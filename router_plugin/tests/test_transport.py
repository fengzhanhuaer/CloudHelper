"""IPv6 HTTP private auth and C TLS listener, without kernel/TUN setup."""
import hashlib,hmac,json,os,socket,ssl,subprocess,tempfile,threading
from pathlib import Path
from http.server import BaseHTTPRequestHandler,HTTPServer
from socketserver import ThreadingMixIn,ThreadingUDPServer,BaseRequestHandler

class HTTP6(ThreadingMixIn,HTTPServer):
    address_family=socket.AF_INET6
    daemon_threads=True
    def server_bind(self):
        self.socket.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1)
        super().server_bind()

subprocess.run(['bin/native/test-transport'],check=True)
class DNS(BaseRequestHandler):
    def handle(self):
        query,udp=self.request
        qtype=int.from_bytes(query[-4:-2],'big')
        address=socket.inet_pton(socket.AF_INET if qtype==1 else socket.AF_INET6,'192.0.2.1' if qtype==1 else '2001:db8::1')
        answer=query[:2]+b'\x81\x80\x00\x01\x00\x01\x00\x00\x00\x00'+query[12:]+b'\xc0\x0c'+query[-4:]+b'\x00\x00\x00\x3c'+len(address).to_bytes(2,'big')+address
        udp.sendto(answer,self.client_address)
with ThreadingUDPServer(('127.0.0.1',0),DNS) as dns:
    threading.Thread(target=dns.serve_forever,daemon=True).start()
    subprocess.run(['bin/native/test-transport','--resolve','127.0.0.1:'+str(dns.server_address[1])],check=True,timeout=7)
    dns.shutdown()
with tempfile.TemporaryDirectory(prefix='chvr-transport6-') as directory:
    root=Path(directory)
    subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-keyout',str(root/'key'),'-out',str(root/'cert'),'-subj','/CN=ipv6.fixture','-addext','subjectAltName=IP:::1'],check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    trust=Path('/usr/local/share/ca-certificates/chvr-transport6-test.crt')
    trust.write_bytes((root/'cert').read_bytes())
    subprocess.run(['update-ca-certificates'],check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    counts={'challenge':0,'business':0}
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*_):pass
        def do_GET(self):
            assert self.headers['Host']==f'[::1]:{self.server.server_port}'
            if self.path.startswith('/api/probe/auth/challenge'):
                counts['challenge']+=1;body={'challenge':'ipv6-fixture-nonce-'+str(counts['challenge'])}
            else:
                nonce=self.headers['X-Probe-Rand'];assert nonce.startswith('ipv6-fixture-nonce-')
                expected=hmac.new(b'fixture-key',('7\n'+nonce+'\nGET\n'+self.path).encode(),hashlib.sha256).hexdigest()
                assert self.headers['X-Probe-Node-Id']=='7' and self.headers['X-Probe-Signature']==expected
                if self.path=='/api/probe/route/config':counts['business']+=1
                body={'ok':True}
            data=json.dumps(body).encode();self.send_response(200);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
    try:
        context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(root/'cert',root/'key')
        with HTTP6(('::1',0),Handler) as server:
            server.socket=context.wrap_socket(server.socket,server_side=True)
            server.socket.settimeout(3)
            threading.Thread(target=server.serve_forever,daemon=True).start()
            subprocess.run(['bin/native/test-transport','--controller',f'https://[::1]:{server.server_port}'],check=True,timeout=15)
            assert counts['challenge']>=1 and counts['business']==1,counts
            server.shutdown()
        with socket.socket(socket.AF_INET6) as reserve:
            reserve.bind(('::1',0));port=reserve.getsockname()[1]
        process=subprocess.Popen(['bin/native/test-transport','--server',str(root/'cert'),str(root/'key'),str(port)],stdout=subprocess.PIPE,universal_newlines=True)
        try:
            assert process.stdout.readline().strip()=='READY'
            client=ssl.create_default_context(cafile=str(root/'cert'))
            with socket.create_connection(('::1',port),timeout=3) as raw:
                with client.wrap_socket(raw,server_hostname='::1') as secured:
                    secured.sendall(f'GET / HTTP/1.1\r\nHost: [::1]:{port}\r\n\r\n'.encode())
                    assert secured.recv(1024).startswith(b'HTTP/1.1 204')
            assert process.wait(timeout=5)==0
        finally:
            if process.poll() is None:process.kill();process.wait()
        print('PASS IPv6 real controller HMAC and V6ONLY TLS carrier listener')
    finally:
        trust.unlink()
        subprocess.run(['update-ca-certificates'],check=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
