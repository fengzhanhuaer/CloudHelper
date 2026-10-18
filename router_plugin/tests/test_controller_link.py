#!/usr/bin/env python3
import hashlib,hmac,json,ssl,subprocess,tempfile,threading,time
from pathlib import Path
from http.server import BaseHTTPRequestHandler,HTTPServer
from socketserver import ThreadingMixIn
class ThreadingHTTPServer(ThreadingMixIn,HTTPServer):
    daemon_threads=True

with tempfile.TemporaryDirectory(prefix='chvr-controller-link-') as directory:
    root=Path(directory)
    for kind,subject in [('primary','controller.fixture'),('direct','self-signed-origin')]:
        subprocess.check_call(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-keyout',str(root/(kind+'.key')),'-out',str(root/(kind+'.crt')),'-subj','/CN='+subject,'-addext','subjectAltName=DNS:'+subject],stderr=subprocess.DEVNULL)
    pub=subprocess.check_output(['openssl','x509','-in',str(root/'direct.crt'),'-pubkey','-noout'])
    der=subprocess.check_output(['openssl','pkey','-pubin','-outform','DER'],input=pub)
    pin=hashlib.sha256(der).hexdigest()
    trust=Path('/usr/local/share/ca-certificates/chvr-link-test.crt');trust.write_bytes((root/'primary.crt').read_bytes())
    subprocess.check_call(['update-ca-certificates'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    used=set();sequence=0;lock=threading.Lock()
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*_):pass
        def do_GET(self):
            global sequence
            if self.path.startswith('/api/probe/auth/challenge'):
                with lock:sequence+=1;nonce='nonce-'+str(sequence)
                if self.server.server_port!=15030:time.sleep(4)
                body={'challenge':nonce}
            else:
                nonce=self.headers.get('X-Probe-Rand','');text='7\n'+nonce+'\nGET\n'+self.path
                expected=hmac.new(b'private-fixture-key',text.encode(),hashlib.sha256).hexdigest()
                with lock:
                    valid=nonce and nonce not in used and self.headers.get('X-Probe-Signature')==expected
                    used.add(nonce)
                if not valid:self.send_error(401);return
                if self.path=='/api/probe/controller/endpoints':
                    body={'version':1,'expires_at':int(time.time())+3600,'endpoints':[{'ip':'2001:db8::1','port':15030,'tls_spki_sha256':pin},{'ip':'192.0.2.1','port':15030,'tls_spki_sha256':pin}]}
                elif self.server.server_port==15030:body={'ok':True}
                else:self.send_error(503);return
            data=json.dumps(body).encode();self.send_response(200);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
    servers=[]
    try:
        for kind,port in [('primary',0),('direct',15030)]:
            server=ThreadingHTTPServer(('127.0.0.1',port),Handler);context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(str(root/(kind+'.crt')),str(root/(kind+'.key')));server.socket=context.wrap_socket(server.socket,server_side=True);servers.append(server);threading.Thread(target=server.serve_forever,daemon=True).start()
        subprocess.check_call(['bin/native/test-controller-link',str(servers[0].server_port)])
    finally:
        for server in servers:server.shutdown();server.server_close()
        trust.unlink()
