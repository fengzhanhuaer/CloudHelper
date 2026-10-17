"""Exercise untrusted tar rejection and actual HTTPS interruption/resume/restart."""
import gzip
import hashlib
from http.server import BaseHTTPRequestHandler, HTTPServer
from socketserver import ThreadingMixIn
import io
from pathlib import Path
import ssl
import subprocess
import tarfile
import tempfile
import threading
import json
import os
import secrets

class ThreadingHTTPServer(ThreadingMixIn, HTTPServer):
    daemon_threads=True

binary = str(Path('bin/native/test-upgrade').resolve())
if os.geteuid()!=0 or not Path('/.dockerenv').exists():
    raise SystemExit('Disposable root test container only')
subprocess.check_call([binary])
with tempfile.TemporaryDirectory(prefix='chvr-upgrade-test-') as work:
    root = Path(work)
    names = ['checksums', 'device-profile', 'install.sh', 'manifest.json', 'release-id',
             'payload/cloudhelper-router-plugin', 'payload/service', 'payload/httpd-bridge.so',
             'payload/web/VirtualRouter.asp', '.valid', 'webs/Module_cloudhelper.asp', 'scripts/uninstall_cloudhelper.sh']
    def package(profile='be86u', extra=None):
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode='w', format=tarfile.USTAR_FORMAT) as archive:
            for name in names:
                body = (profile+'\n').encode() if name == 'device-profile' else b'fixture'
                info = tarfile.TarInfo('cloudhelper/'+name); info.size = len(body)
                archive.addfile(info, io.BytesIO(body))
            if extra:
                archive.addfile(extra, io.BytesIO(b''))
        return gzip.compress(raw.getvalue())
    def unpack(body, succeeds):
        packed = root/'package.tar.gz'; packed.write_bytes(body)
        with tempfile.TemporaryDirectory(dir=work) as dest:
            result = subprocess.call([binary, 'unpack', str(packed), dest, 'be86u'])
            assert (result == 0) == succeeds
    unpack(package(), True)
    unpack(package('ax56uv2'), False)
    unpack(package()[:-8], False)
    unpack(package()+b'bad-gzip-footer', False)
    for name in ['cloudhelper/../escape', 'cloudhelper/install.sh', '/tmp/escape']:
        unpack(package(extra=tarfile.TarInfo(name)), False)
    link = tarfile.TarInfo('cloudhelper/other'); link.type=tarfile.SYMTYPE; link.linkname='/etc/passwd'
    unpack(package(extra=link), False)
    # A post-start installer failure must restore the old active release,
    # even when failure happens after the installer's own startup guard.
    base=Path('/jffs/cloudhelper-router');base.mkdir(parents=True)
    active=base/'active';active.write_text('a'*16+'\n')
    progress=Path('/var/run/cloudhelper-router');progress.mkdir(parents=True,exist_ok=True)
    stage=Path('/tmp/chvr-upgrade.'+secrets.token_hex(3));stage.mkdir(mode=0o700)
    try:
        (stage/'cloudhelper').mkdir()
        packed=stage/'package.tar.gz';packed.write_bytes(b'fixture-verified-archive')
        (stage/'job.json').write_text(json.dumps({'sha256':hashlib.sha256(packed.read_bytes()).hexdigest(),'version':'v0.4.70'}))
        (stage/'cloudhelper/install.sh').write_text('#!/bin/sh\nif test "${1:-}" = --rollback;then printf "%s\\n" '+('a'*16)+' > /jffs/cloudhelper-router/active;exit 0;fi\nprintf "%s\\n" '+('b'*16)+' > /jffs/cloudhelper-router/active\nexit 1\n')
        rc=subprocess.call([str(Path('bin/native/cloudhelper-router-plugin').resolve()),'--apply-upgrade',str(stage)])
        assert rc!=0 and active.read_text().strip()=='a'*16
        assert not stage.exists() and json.loads((progress/'upgrade.json').read_text())['phase']=='failed'
    finally:
        active.unlink();base.rmdir();base.parent.rmdir()
    # The fixture CA is installed only in this disposable test container.
    cert, key = root/'cert.pem', root/'key.pem'
    subprocess.check_call(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                           '-keyout', str(key), '-out', str(cert), '-subj', '/CN=127.0.0.1',
                           '-addext', 'subjectAltName=IP:127.0.0.1'], stderr=subprocess.DEVNULL)
    trust=Path('/usr/local/share/ca-certificates/chvr-test.crt');trust.write_bytes(cert.read_bytes())
    subprocess.check_call(['update-ca-certificates'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    body=bytes(range(256))*64
    class Handler(BaseHTTPRequestHandler):
        attempts={}
        def log_message(self, *_): pass
        def do_GET(self):
            attempt=self.attempts.get(self.path,0);self.attempts[self.path]=attempt+1
            offset=int(self.headers.get('Range','bytes=0-').split('=')[1].split('-')[0])
            if attempt == 0:
                self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers()
                self.wfile.write(body[:1500]);self.wfile.flush();self.close_connection=True;return
            assert offset==1500, self.headers
            if self.path=='/restart':
                self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body);return
            self.send_response(206)
            first=offset+1 if self.path=='/bad-range' else offset
            self.send_header('Content-Range','bytes %d-%d/%d'%(first,len(body)-1,len(body)))
            self.send_header('Content-Length',str(len(body)-offset));self.end_headers();self.wfile.write(body[offset:])
    server=ThreadingHTTPServer(('127.0.0.1',0),Handler)
    context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(str(cert),str(key))
    server.socket=context.wrap_socket(server.socket,server_side=True)
    threading.Thread(target=server.serve_forever,daemon=True).start()
    try:
        for mode in ['resume','restart','bad-range']:
            target=root/(mode+'.bin')
            rc=subprocess.call([binary,'download','https://127.0.0.1:%d/%s'%(server.server_port,mode),str(target),str(len(body))])
            assert (rc==0)==(mode!='bad-range')
            if rc==0:assert hashlib.sha256(target.read_bytes()).digest()==hashlib.sha256(body).digest()
            else:assert target.stat().st_size==1500
    finally:
        server.shutdown();server.server_close();trust.unlink()
print('PASS: strict tar/model/CRC, real HTTPS partial download, Range resume, ignored Range restart, wrong range rejection')
