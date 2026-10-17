#!/usr/bin/env python3
"""Real HTTPS, TLS DNS, UDP and TCP DNS against the C resolver, no public DNS."""
import http.server
import json
import os
from pathlib import Path
import socket
import ssl
import struct
import subprocess
import sys
import tempfile
import threading

binary=str(Path(sys.argv[1]).resolve())
ip=socket.gethostbyname(socket.gethostname())
counts={'doh':0,'dot':0,'dns':0,'tcp':0}
mode={'stage':'doh','truncate':False}

def answer(q, fail=False):
    return q[:2]+struct.pack('!HHHHH',0x8182 if fail else 0x8180,1,0 if fail else 1,0,0)+q[12:]+(b'' if fail else b'\xc0\x0c\x00\x01\x00\x01\x00\x00\x00\x1e\x00\x04\xcb\x00\x71\x05')

def exact(conn,n):
    out=b''
    while len(out)<n:
        b=conn.recv(n-len(out))
        if not b:raise EOFError()
        out+=b
    return out

with tempfile.TemporaryDirectory() as tmp:
    tmp=Path(tmp);cert=tmp/'ca.pem';key=tmp/'key.pem'
    subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-subj','/CN=DNS fixture','-days','1','-addext','subjectAltName=IP:'+ip,'-keyout',str(key),'-out',str(cert)],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.load_cert_chain(cert,key)
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            counts['doh']+=1;q=self.rfile.read(int(self.headers['Content-Length']))
            assert self.headers['Content-Type']=='application/dns-message'
            self.send_response(200 if mode['stage']=='doh' else 503);self.send_header('Content-Type','application/dns-message');self.end_headers();self.wfile.write(answer(q))
        def log_message(self,*args):pass
    httpd=http.server.HTTPServer((ip,0),Handler);httpd.socket=ctx.wrap_socket(httpd.socket,server_side=True)
    threading.Thread(target=httpd.serve_forever,daemon=True).start()
    dot=socket.socket();dot.bind((ip,0));dot.listen(8)
    tcp=socket.socket();tcp.bind((ip,0));tcp.listen(8)
    udp=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);udp.bind(tcp.getsockname())
    def stream(listener,secure):
        while True:
            conn,_=listener.accept()
            try:
                if secure:conn=ctx.wrap_socket(conn,server_side=True)
                conn.settimeout(3);size=struct.unpack('!H',exact(conn,2))[0];q=exact(conn,size)
                counts['dot' if secure else 'tcp']+=1
                reply=answer(q,secure and mode['stage']!='dot');conn.sendall(struct.pack('!H',len(reply))+reply)
            except (ssl.SSLError,EOFError,OSError):pass
            finally:conn.close()
    def datagram():
        while True:
            q,peer=udp.recvfrom(4096);counts['dns']+=1;reply=answer(q)
            if mode['truncate']:reply=reply[:2]+bytes([reply[2]|2])+reply[3:]
            udp.sendto(reply,peer)
    threading.Thread(target=stream,args=(dot,True),daemon=True).start()
    threading.Thread(target=stream,args=(tcp,False),daemon=True).start()
    threading.Thread(target=datagram,daemon=True).start()
    config={'dns_upstreams':{'doh':f'https://{ip}:{httpd.server_port}/dns-query','dot':f'{ip}:{dot.getsockname()[1]}','dns':f'{ip}:{tcp.getsockname()[1]}'}}
    path=tmp/'config.json';path.write_text(json.dumps(config))
    env=dict(os.environ,SSL_CERT_FILE=str(cert))
    for stage,want in [('doh',{'doh':1,'dot':0,'dns':0,'tcp':0}),('dot',{'doh':1,'dot':1,'dns':0,'tcp':0}),('dns',{'doh':1,'dot':1,'dns':1,'tcp':1})]:
        counts.update({k:0 for k in counts});mode.update(stage=stage,truncate=stage=='dns')
        result=subprocess.run([binary,str(path)],env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,universal_newlines=True,timeout=15)
        assert result.returncode==0,(stage,result.returncode,result.stderr)
        assert counts==want,(stage,counts,want)
    # An untrusted certificate fails both encrypted transports and reaches plain DNS.
    counts.update({k:0 for k in counts});mode.update(stage='doh',truncate=False)
    result=subprocess.run([binary,str(path)],stdout=subprocess.PIPE,stderr=subprocess.PIPE,universal_newlines=True,timeout=15)
    assert result.returncode==0 and counts['dns']==1 and counts['doh']==0 and counts['dot']==0,counts
    # Every nonempty combination tries only its first enabled, healthy transport.
    for mask in range(1,8):
        flags={'doh_enabled':bool(mask&1),'dot_enabled':bool(mask&2),'dns_enabled':bool(mask&4)}
        config['dns_upstreams'].update(flags);path.write_text(json.dumps(config))
        stage='doh' if mask&1 else 'dot' if mask&2 else 'dns'
        counts.update({k:0 for k in counts});mode.update(stage=stage,truncate=False)
        result=subprocess.run([binary,str(path)],env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,universal_newlines=True,timeout=15)
        assert result.returncode==0,(mask,result.returncode,result.stderr)
        want={k:int(k==stage) for k in counts};assert counts==want,(mask,counts,want)
    httpd.shutdown()
print('PASS: C DoH -> DoT -> ordinary DNS fallback, TLS trust, DNS framing and UDP truncation -> TCP')
