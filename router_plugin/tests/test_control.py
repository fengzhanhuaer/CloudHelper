"""Real authenticated TLS/WS/yamux configuration sync in a disposable container."""
import base64
import hashlib
import hmac
import http.server
import json
import os
from pathlib import Path
import socketserver
import ssl
import struct
import subprocess
import tempfile
import threading

assert os.environ.get('CHVR_DISPOSABLE_TEST') == '1' and Path('/.dockerenv').exists()
with tempfile.TemporaryDirectory(prefix='chvr-control-') as work:
    root = Path(work)
    key, cert = root/'key.pem', root/'cert.pem'
    subprocess.check_call(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                           '-subj', '/CN=control fixture', '-addext', 'subjectAltName=IP:127.0.0.1',
                           '-keyout', str(key), '-out', str(cert)], stderr=subprocess.DEVNULL)
    bundle = Path('/etc/ssl/certs/ca-certificates.crt')
    original_bundle = bundle.read_bytes()
    bundle.write_bytes(original_bundle + cert.read_bytes())
    state = {'upgrades': 0, 'reports': 0, 'configs': 0, 'chunks': 0, 'public_reported': False}
    fixture = root/'telemetry.json'
    fixture.write_bytes(subprocess.check_output(['bin/native/test-telemetry', '--json']))
    errors = []

    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version = 'HTTP/1.1'
        def log_message(self, *_): pass

        def send_mux(self, kind, length, data=b''):
            mux = struct.pack('!BBHII', 0, kind, 0, 1, length) + data
            header = bytes([0x82, len(mux)]) if len(mux) < 126 else b'\x82\x7e' + struct.pack('!H', len(mux))
            self.wfile.write(header + mux)
            self.wfile.flush()

        def send_control(self, message):
            data = json.dumps(message).encode() + b'\n'
            self.send_mux(0, len(data), data)

        def do_GET(self):
            try:
                self.handle_get()
            except (OSError, ssl.SSLError):
                pass
            except Exception as error:
                errors.append(repr(error))

        def handle_get(self):
            if self.path == '/fixture-public-ip':
                self.send_response(200)
                self.send_header('Content-Length', '8')
                self.end_headers()
                self.wfile.write(b'1.2.3.4\n')
                return
            if self.path.startswith('/api/probe/auth/challenge'):
                body = {'challenge': 'fixture-nonce'}
            else:
                expected = hmac.new(b'fixture-only-key', ('7\nfixture-nonce\nGET\n'+self.path).encode(), hashlib.sha256).hexdigest()
                assert self.headers['X-Probe-Node-Id'] == '7'
                assert hmac.compare_digest(self.headers['X-Probe-Signature'], expected)
                if self.path == '/api/probe':
                    state['upgrades'] += 1
                    accept = base64.b64encode(hashlib.sha1((self.headers['Sec-WebSocket-Key']+'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
                    self.send_response(101)
                    self.send_header('Upgrade', 'websocket')
                    self.send_header('Connection', 'Upgrade')
                    self.send_header('Sec-WebSocket-Accept', accept)
                    self.end_headers()
                    self.send_control({'type': 'ack'})
                    self.send_control({'type': 'route_config_sync'})
                    pending = b''
                    while True:
                        h = self.rfile.read(2)
                        if len(h) != 2: return
                        assert h[1] & 128
                        n = h[1] & 127
                        if n == 126: n = struct.unpack('!H', self.rfile.read(2))[0]
                        if n == 127: n = struct.unpack('!Q', self.rfile.read(8))[0]
                        mask = self.rfile.read(4)
                        data = self.rfile.read(n)
                        data = bytes(b ^ mask[i % 4] for i, b in enumerate(data))
                        if len(data) >= 12 and data[1] == 0:
                            size = struct.unpack('!I', data[8:12])[0]
                            assert len(data) == size + 12
                            state['chunks'] += 1
                            self.send_mux(1, size)
                            pending += data[12:]
                            if b'\n' not in pending: continue
                            line, pending = pending.split(b'\n', 1)
                            report = json.loads(line)
                            assert report['type'] == 'report' and report['node_id'] == '7'
                            assert 'ipv6' in report and report['timestamp'].endswith('Z')
                            state['public_reported'] |= '1.2.3.4' in report['ipv4']
                            state['reports'] += 1
                            if state['reports'] <= 7:
                                assert len(line) > 16384
                                assert len(report['relay_status']) == 8
                                assert len(report['virtual_router_buffers']['items']) == 64
                            else:
                                assert report['relay_status'] == []
                                assert report['virtual_router_buffers']['items'] == []
                            self.send_control({'type': 'ack'})
                            if state['reports'] < 7:
                                self.send_control({'type': 'report_once'})
                            elif state['reports'] == 7:
                                self.send_control({'type': 'virtual_router_buffers_reset'})
                    return
                if self.path != '/api/probe/route/config':
                    self.send_error(404)
                    return
                state['configs'] += 1
                body = {'node_id': '7', 'expected_node_kind': 'asus_56uv2', 'revision': 2,
                        'virtual_router': {'fake_ip_cidr': '198.18.0.0/15',
                                          'probe_ips': [{'node_id': '7', 'ip': '198.18.0.7'}],
                                          'topology_rules': [], 'route_rules': [
                                              {'id': 'block', 'action': 'reject', 'entries': ['domain:blocked.example']}],
                                          'updated_at': 'new'}}
            data = json.dumps(body).encode()
            self.send_response(200)
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
        daemon_threads = True
    server = Server(('127.0.0.1', 0), Handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(cert, key)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        subprocess.check_call(['bin/native/test-control', 'https://127.0.0.1:'+str(server.server_port), str(fixture)], timeout=50)
        assert not errors, errors
        assert state['upgrades'] == 1 and state['reports'] == 8 and state['configs'] >= 1, state
        assert state['chunks'] > state['reports'], state
        assert state['public_reported'], state
        print('PASS: one control stream retained across real configuration sync and repeated reports')
    finally:
        server.shutdown()
        server.server_close()
        bundle.write_bytes(original_bundle)
