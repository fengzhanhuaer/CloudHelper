"""Observe real ClientHello SNI and verify identity checks on both TLS paths."""
import hashlib
import os
from pathlib import Path
import socketserver
import ssl
import socket
import time
import subprocess
import tempfile
import threading

with tempfile.TemporaryDirectory(prefix='chvr-relay-tls-') as directory:
    root = Path(directory)
    for name in ('trusted', 'other'):
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                        '-days', '1', '-keyout', str(root / (name + '.key')),
                        '-out', str(root / (name + '.crt')), '-subj', '/CN=relay.fixture',
                        '-addext', 'subjectAltName=DNS:relay.fixture,DNS:api_copilot_fixture.example,IP:::1'],
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

    class Server6(Server):
        address_family = socket.AF_INET6
        def server_bind(self):
            self.socket.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
            super().server_bind()

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

    with Server6(('::1', 0), Handler) as server:
        threading.Thread(target=server.serve_forever,daemon=True).start()
        environment=dict(os.environ,SSL_CERT_FILE=str(root/'trusted.crt'),SSL_CERT_DIR=str(root/'empty-ca-directory'),CHVR_TLS_TEST_ADDRESS='::1')
        for mode,host,key_pin,expected_sni,accepted in [
            ('relay','relay.fixture',pin,None,True),
            ('relay','relay.fixture','0'*64,None,False),
            ('normal','::1','',None,True),
        ]:
            observed.clear()
            result=subprocess.run(['bin/native/test-tls-connect',mode,host,str(server.server_address[1]),key_pin],env=environment,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=7)
            assert result.returncode==(0 if accepted else 2),(result.stdout,result.stderr)
            assert observed==[expected_sni],observed
            print('PASS IPv6:',mode,host,'pin/identity',accepted)
        class Stall(socketserver.BaseRequestHandler):
            def handle(self):time.sleep(2)
        with Server(('127.0.0.1',server.server_address[1]),Stall) as stalled:
            threading.Thread(target=stalled.serve_forever,daemon=True).start()
            environment.update(CHVR_TLS_TEST_ADDRESS='127.0.0.1',CHVR_TLS_TEST_ALTERNATE='::1')
            expected_sni=None;started=time.monotonic()
            result=subprocess.run(['bin/native/test-tls-connect','relay','relay.fixture',str(server.server_address[1]),pin],env=environment,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=7)
            assert result.returncode==0,(result.stdout,result.stderr)
            assert time.monotonic()-started<1.5,'stalled first TLS address blocked IPv6'
            print('PASS IPv6 fallback wins while IPv4 TLS stalls; losing sockets closed')
            stalled.shutdown()
        server.shutdown()
