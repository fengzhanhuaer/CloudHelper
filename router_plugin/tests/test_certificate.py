"""Real delayed HTTPS/HMAC checks, restricted to a disposable test container."""
import hashlib
import hmac
import http.server
import json
import os
from pathlib import Path
import ssl
import socketserver
import subprocess
import sys
import tempfile
import threading
import time

assert os.environ.get('CHVR_DISPOSABLE_TEST') == '1' and Path('/.dockerenv').exists()
with tempfile.TemporaryDirectory(prefix='chvr-cert-test-') as work:
    root = Path(work)
    key, cert = root/'key.pem', root/'cert.pem'
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                    '-subj', '/CN=router isolated certificate test', '-addext', 'subjectAltName=IP:127.0.0.1',
                    '-keyout', str(key), '-out', str(cert)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    # Only this throwaway container's bundle is changed, never a host/router CA.
    Path('/etc/ssl/certs/ca-certificates.crt').write_bytes(cert.read_bytes())
    received = []
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args): pass
        def do_GET(self):
            received.append(self.path)
            if self.path == '/api/probe/auth/challenge?node_id=7':
                body, code = {'challenge':'certificate-fixture'}, 200
            else:
                expected = hmac.new(b'fixture-only-key', ('7\ncertificate-fixture\nGET\n'+self.path).encode(), hashlib.sha256).hexdigest()
                assert self.headers['X-Probe-Node-Id'] == '7'
                assert self.headers['X-Probe-Rand'] == 'certificate-fixture'
                assert hmac.compare_digest(self.headers['X-Probe-Signature'], expected)
                if self.path in ['/api/probe/certificate', '/slow']: time.sleep(12)
                body, code = ({'error':'fixture upstream failure'}, 503) if self.path == '/fail' else ({'node_id':'7'}, 200)
            try:
                self.send_response(code);self.send_header('Content-Type','application/json');self.end_headers()
                self.wfile.write(json.dumps(body).encode())
            except (OSError, ssl.SSLError): pass  # Expected timed-out/cancelled clients.
    class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
        daemon_threads = True
    server = Server(('127.0.0.1', 0), Handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(cert, key)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    thread = threading.Thread(target=server.serve_forever, daemon=True);thread.start()
    try:
        subprocess.run([sys.argv[1], 'https://127.0.0.1:'+str(server.server_port)], check=True, timeout=55)
        assert received.count('/api/probe/certificate') == 3 and received.count('/slow') == 1
        assert received.count('/api/probe/auth/challenge?node_id=7') == 5
        print('PASS: independent HTTPS server checked signatures and no extra/replayed certificate request')
    finally:
        server.shutdown();server.server_close();thread.join()
