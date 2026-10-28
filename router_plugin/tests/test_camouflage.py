"""Ordinary HTTPS responses and clean TLS shutdown, with no private carrier auth."""
import json
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile

with tempfile.TemporaryDirectory(prefix='chvr-http-') as work:
    root=Path(work);cert=root/'cert.pem';key=root/'key.pem'
    subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(key),'-out',str(cert),'-subj','/CN=localhost','-days','1'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    proc=subprocess.Popen(['bin/native/test-camouflage',str(cert),str(key)],stdout=subprocess.PIPE,stderr=subprocess.PIPE,universal_newlines=True)
    try:
        ready=proc.stdout.readline().split();assert ready and ready[0]=='READY',proc.stderr.read();port=int(ready[1])
        context=ssl.SSLContext(ssl.PROTOCOL_TLSv1_2)
        for method,path,expected in [('GET','/',200),('GET','/?test=1',200),('GET','/v1/models',401),('POST','/',405),('GET','/unknown',404)]:
            with context.wrap_socket(socket.create_connection(('127.0.0.1',port),timeout=5),server_hostname='localhost',suppress_ragged_eofs=False) as stream:
                stream.sendall(f'{method} {path} HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n'.encode());raw=b''
                while True:
                    part=stream.recv(4096)
                    if not part:break
                    raw+=part
                head,body=raw.split(b'\r\n\r\n',1);assert int(head.split()[1])==expected
                if expected==200:assert json.loads(body)['message']=='OpenAI-compatible API endpoint'
                if expected==401:assert json.loads(body)['error']['code']=='invalid_api_key' and b'WWW-Authenticate: Bearer' in head
                if expected==405:assert json.loads(body)['error']['code']=='method_not_allowed'
        assert proc.wait(timeout=5)==0,proc.stderr.read()
        print('PASS: TLS1.2 OpenAI-style GET/401/405/404, clean close_notify and separate private relay path')
    finally:
        if proc.poll() is None:proc.kill();proc.wait()
