"""Install/update/rollback/uninstall with native test executable in a disposable container."""
import hashlib
import http.client
import os
from pathlib import Path
import shutil
import socket
import json
import subprocess
import sys
import tempfile

if os.environ.get('CHVR_DISPOSABLE_TEST')!='1' or not Path('/.dockerenv').exists():
    raise SystemExit('Disposable privileged container only')
def run(*args):
    try:return subprocess.check_output(args,stderr=subprocess.STDOUT,universal_newlines=True)
    except subprocess.CalledProcessError as error:
        print(error.output);raise
fixture=Path('/tmp/chvr-install-bin');fixture.mkdir()
profile=sys.argv[1] if len(sys.argv)>1 else 'ax56uv2'
assert profile in ('ax56uv2','be86u')
model,odm,arch,fs,web_hash=('RT-AX55','RT-AX56U_V2','armv7l','jffs2','144f9083c6ae57632cf59b4012d5ad46aff6f4f64012ce3fed50e0f1b8ebf6d1') if profile=='ax56uv2' else ('RT-BE86U','','aarch64','ubifs','8804d44c2633046d7740d356e7bdcd5e46b1d2b3ac9b1e967dda01e99150823e')
(fixture/'nvram').write_text('#!/bin/sh\ncase "$2" in productid) echo '+model+';; odmpid) echo '+odm+';; lan_ipaddr) echo 192.168.52.1;; lan_ifname) echo lo;; esac\n')
(fixture/'uname').write_text('#!/bin/sh\necho '+arch+'\n')
(fixture/'service').write_text('#!/bin/sh\npkill -x httpd || true\n/usr/sbin/httpd </dev/null >/dev/console 2>&1 &\n')
(fixture/'openssl').write_text('#!/bin/sh\ncase "$3" in /usr/sbin/httpd|/usr/local/cloudhelper-router/httpd) if test -f /tmp/chvr-bad-httpd;then echo "SHA256= unsupported";else echo "SHA256= '+web_hash+'";fi;; *) exec /usr/bin/openssl "$@";; esac\n')
(fixture/'cru').write_text('#!/bin/sh\nexit 0\n')
(fixture/'awk').write_text('#!/bin/sh\nif test "$1" = -v && test "$2" = fs='+fs+';then exit 0;fi\nexec /usr/bin/awk "$@"\n')
for file in fixture.iterdir():file.chmod(0o700)
os.environ['PATH']=str(fixture)+':'+os.environ['PATH']
run('ip','addr','add','192.168.52.1/32','dev','lo')
target=Path('/www/require/modules/menuTree.js');target.parent.mkdir(parents=True)
original=Path('/stock/www/require/modules/menuTree.js').read_bytes()
shutil.copyfile('bin/native/httpd-fixture','/usr/sbin/httpd')
Path('/usr/sbin/httpd').chmod(0o700)
target.write_bytes(original)
base=Path('/jffs/cloudhelper-router');base.parent.mkdir()
# Reproduce the real stock /usr/local layout in this disposable container.
saved_local=Path(tempfile.mkdtemp(prefix='chvr-original-local-'))
for child in Path('/usr/local').iterdir():shutil.move(str(child),str(saved_local/child.name))
Path('/usr/local/share').symlink_to('/tmp/share')
def status():
    with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as client:
        client.settimeout(5);client.connect('/var/run/cloudhelper-router/admin.sock')
        body=b'{"action":"status"}'
        client.sendall(b'POST / HTTP/1.0\r\nContent-Length: '+str(len(body)).encode()+b'\r\n\r\n'+body)
        raw=b''
        while True:
            data=client.recv(65536)
            if not data:break
            raw+=data
        return json.loads(raw.split(b'\r\n\r\n',1)[1])['ok']
with tempfile.TemporaryDirectory(prefix='chvr-install-') as directory:
    root=Path(directory)
    shutil.copyfile('jffs/install.sh',str(root/'install.sh'))
    (root/'device-profile').write_text(profile+'\n')
    (root/'payload/web').mkdir(parents=True)
    for source,name in [('bin/native/cloudhelper-router-plugin','cloudhelper-router-plugin'),('jffs/service','service'),('web/VirtualRouter.asp','web/VirtualRouter.asp'),('bin/native/httpd-bridge.so','httpd-bridge.so')]:
        shutil.copyfile(source,str(root/'payload'/name))
    (root/'payload/cloudhelper-router-plugin').chmod(0o700)
    (root/'payload/service').chmod(0o700)
    def prepare(release):
        (root/'release-id').write_text(release+'\n')
        lines=[]
        for name in ('cloudhelper-router-plugin','service','httpd-bridge.so','web/VirtualRouter.asp'):
            path=root/'payload'/name
            lines.append(hashlib.sha256(path.read_bytes()).hexdigest()+' payload/'+name+'\n')
        (root/'checksums').write_text(''.join(lines))
    try:
        prepare('a'*16)
        (root/'device-profile').write_text(('be86u' if profile=='ax56uv2' else 'ax56uv2')+'\n')
        assert subprocess.call(['sh',str(root/'install.sh')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)!=0
        assert not base.exists()
        (root/'device-profile').write_text(profile+'\n')
        if profile=='be86u':
            plugin=str(root/'payload/cloudhelper-router-plugin')
            foreign=Path('/usr/local/foreign');foreign.write_text('retain')
            assert subprocess.call([plugin,'--runtime-mount','local'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)!=0
            assert foreign.read_text()=='retain'
            foreign.unlink()
            for op in ('arbitrary-path','unlocal','unmenu','unweb'):
                assert subprocess.call([plugin,'--runtime-mount',op],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)!=0
            foreign=root/'foreign-menu';foreign.write_text('third party menu')
            run('mount','--bind',str(foreign),str(target))
            assert subprocess.call([plugin,'--runtime-mount','unmenu'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)!=0
            assert target.read_text()=='third party menu'
            run('umount',str(target))
            Path('/tmp/chvr-bad-httpd').touch()
            assert subprocess.call(['sh',str(root/'install.sh')],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)!=0
            assert not base.exists() and target.read_bytes()==original
            Path('/tmp/chvr-bad-httpd').unlink()
        run('sh',str(root/'install.sh'))
        assert status()
        assert 'CHVR_JFFS_MENU' in target.read_text(encoding='utf-8')
        assert not (base/'admin.key').exists()
        assert b'CHVR_HTTPD_WRAPPER' in Path('/usr/sbin/httpd').read_bytes()
        key=b'legacy retained'
        (base/'admin.key').write_bytes(key)
        prepare('b'*16)
        run('sh',str(root/'install.sh'))
        assert (base/'active').read_text().strip()=='b'*16 and status()
        assert (base/'admin.key').read_bytes()==key
        run('sh',str(root/'install.sh'),'--rollback')
        assert (base/'active').read_text().strip()=='a'*16 and status()
        run('sh',str(root/'install.sh'),'--uninstall')
        assert not (base/'active').exists() and not (base/'service').exists()
        assert (base/'admin.key').read_bytes()==key and target.read_bytes()==original
        assert not list(base.glob('**/*.log'))
        assert Path('/usr/local/share').is_symlink()
        assert list(Path('/usr/local').iterdir())==[Path('/usr/local/share')]
    finally:
        if (base/'service').exists():subprocess.call(['sh',str(base/'service'),'stop'])
print('PASS: install, Unix-only management, stock web extension mount, actual stock menu, update, credential retention, rollback and uninstall')
