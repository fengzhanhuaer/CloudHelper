"""Observe real ClientHello SNI and verify identity checks on both TLS paths."""
import hashlib
import os
from pathlib import Path
import socketserver
import ssl
import subprocess
import tempfile
import threading

with tempfile.TemporaryDirectory(prefix='chvr-relay-tls-') as directory:
    root = Path(directory)
    for name in ('trusted', 'other'):
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                        '-days', '1', '-keyout', str(root / (name + '.key')),
                        '-out', str(root / (name + '.crt')), '-subj', '/CN=relay.fixture',
                        '-addext', 'subjectAltName=DNS:relay.fixture,DNS:api_copilot_fixture.example'],
                       check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    public = subprocess.check_output(['openssl', 'x509', '-in', str(root / 'trusted.crt'), '-pubkey', '-noout'])
    der = subprocess.check_output(['openssl', 'pkey', '-pubin', '-outform', 'DER'], input=public)
    pin = hashlib.sha256(der).hexdigest()
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(root / 'trusted.crt', root / 'trusted.key')
    observed = []
    expected_sni = None

    def on_sni(sock, name, ctx):
        observed.append(name)
        if name != expected_sni:
            return ssl.ALERT_DESCRIPTION_UNRECOGNIZED_NAME

    context.set_servername_callback(on_sni)

    class Handler(socketserver.BaseRequestHandler):
        def handle(self):
            self.request.settimeout(4)
            try:
                with context.wrap_socket(self.request, server_side=True) as stream:
                    stream.recv(1)
            except (ssl.SSLError, ConnectionError):
                pass  # Negative identity tests abort the handshake or close.

    class Server(socketserver.ThreadingTCPServer):
        daemon_threads = True

    with Server(('127.0.0.1', 0), Handler) as server:
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            cases = [
                ('relay', 'relay.fixture', '', 'trusted', None, True),
                ('relay', 'wrong.fixture', '', 'trusted', None, False),
                ('relay', 'relay.fixture', '', 'other', None, False),
                ('relay', 'relay.fixture', pin, 'other', None, True),
                ('relay', 'relay.fixture', '0' * 64, 'trusted', None, False),
                ('normal', 'relay.fixture', '', 'trusted', 'relay.fixture', True),
                ('normal', 'wrong.fixture', '', 'trusted', 'wrong.fixture', False),
                ('relay', 'api_copilot_fixture.example', '', 'trusted', 'api_copilot_fixture.example', True),
            ]
            for mode, host, key_pin, trust, expected_sni, accepted in cases:
                observed.clear()
                environment = dict(os.environ, SSL_CERT_FILE=str(root / (trust + '.crt')),
                                   SSL_CERT_DIR=str(root / 'empty-ca-directory'))
                result = subprocess.run(['bin/native/test-tls-connect', mode, host,
                                         str(server.server_address[1]), key_pin],
                                        env=environment, universal_newlines=True,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=7)
                assert result.returncode == (0 if accepted else 2), (mode, host, result)
                assert observed == [expected_sni], (mode, host, observed, expected_sni)
                print('PASS:', mode, host, 'SNI=' + str(expected_sni),
                      'accepted' if accepted else 'identity rejected')
        finally:
            server.shutdown()
            worker.join()
