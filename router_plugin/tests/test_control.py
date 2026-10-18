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
    state = {'upgrades': 0, 'reports': 0, 'configs': 0}
    errors = []

    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version = 'HTTP/1.1'
        def log_message(self, *_): pass

        def send_control(self, message):
            data = json.dumps(message).encode() + b'\n'
            mux = struct.pack('!BBHII', 0, 0, 0, 1, len(data)) + data
            header = bytes([0x82, len(mux)]) if len(mux) < 126 else b'\x82\x7e' + struct.pack('!H', len(mux))
            self.wfile.write(header + mux)
            self.wfile.flush()

        def do_GET(self):
            try:
                self.handle_get()
            except (OSError, ssl.SSLError):
                pass
            except Exception as error:
                errors.append(repr(error))

        def handle_get(self):
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
                            state['reports'] += 1
                            self.send_control({'type': 'ack'})
                            if state['reports'] == 1:
                                self.send_control({'type': 'report_once'})
                                self.send_control({'type': 'route_config_sync'})
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
        subprocess.check_call(['bin/native/test-control', 'https://127.0.0.1:'+str(server.server_port)], timeout=50)
        assert not errors, errors
        assert state['upgrades'] == 1 and state['reports'] >= 2 and state['configs'] >= 1, state
        print('PASS: one control stream retained across real configuration sync and repeated reports')
    finally:
        server.shutdown()
        server.server_close()
        bundle.write_bytes(original_bundle)
