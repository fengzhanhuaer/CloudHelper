"""Install/update/rollback/uninstall with native test executable in a disposable container."""
import hashlib
import http.client
import os
from pathlib import Path
import shutil
import socket
import json
import secrets
import subprocess
import sys
import tempfile
import time

if os.environ.get('CHVR_DISPOSABLE_TEST')!='1' or not Path('/.dockerenv').exists():
    raise SystemExit('Disposable privileged container only')
def run(*args):
    try:return subprocess.check_output(args,stderr=subprocess.STDOUT,universal_newlines=True)
    except subprocess.CalledProcessError as error:
        print(error.output);raise
fixture=Path('/tmp/chvr-install-bin');fixture.mkdir()
profile=sys.argv[1] if len(sys.argv)>1 else 'ax56uv2'
koolcenter=profile in ('be86u-koolcenter','ax56uv2-koolshare')
if koolcenter:profile='ax56uv2' if profile.startswith('ax56uv2') else 'be86u'
assert profile in ('ax56uv2','be86u')
model,odm,arch,fs,web_hash=('RT-AX55','RT-AX56U_V2','armv7l','jffs2','144f9083c6ae57632cf59b4012d5ad46aff6f4f64012ce3fed50e0f1b8ebf6d1') if profile=='ax56uv2' else ('RT-BE86U','','aarch64','ubifs','8804d44c2633046d7740d356e7bdcd5e46b1d2b3ac9b1e967dda01e99150823e')
if koolcenter:web_hash='764716012e664f64f1857ba1027a269d73899c09d4b10f5323bd4fc438997004' if profile=='ax56uv2' else '1b7148dabccb0954b13b8bf70f853afbcd133160cf9f4cb201789bccdb5143e8'
(fixture/'nvram').write_text('#!/bin/sh\ncase "$2" in productid) echo '+model+';; odmpid) echo '+odm+';; lan_ipaddr) echo 192.168.52.1;; lan_ifname) echo lo;; extendno) echo '+('future_99999_koolcenter' if koolcenter else 'future_99999')+';; jffs2_scripts) echo 1;; esac\n')
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
with Path('/usr/sbin/httpd').open('ab') as future:
    future.write(b'future-firmware-independent-layout-test')
Path('/usr/sbin/httpd').chmod(0o700)
target.write_bytes(original)
base=Path('/jffs/cloudhelper-router');base.parent.mkdir()
if koolcenter:
    Path('/koolshare/init.d').mkdir(parents=True)
    Path('/koolshare/bin').mkdir()
    Path('/koolshare/webs').mkdir()
    Path('/koolshare/scripts').mkdir()
    (fixture/'dbus').write_text('#!/bin/sh\ncase "$1" in set) printf "%s\\n" "$2" >> /tmp/chvr-dbus;; remove) printf "remove %s\\n" "$2" >> /tmp/chvr-dbus;; esac\n')
    (fixture/'dbus').chmod(0o700)
    Path('/usr/bin/dbus').symlink_to(fixture/'dbus')
    Path('/jffs/scripts').mkdir()
    for phase,name in [('V','start'),('T','stop')]:
        hook=Path('/koolshare/bin/ks-services-'+name+'.sh')
        hook.write_text('#!/bin/sh\nfor script in /koolshare/init.d/'+phase+'*.sh;do /bin/sh "$script";done\n')
        hook.chmod(0o700)
        Path('/jffs/scripts/services-'+name).write_text('#!/bin/sh\n/koolshare/bin/ks-services-'+name+'.sh\n')
    other=Path('/koolshare/init.d/V01other.sh');other.write_text('#!/bin/sh\ntouch /tmp/chvr-other-started\n');other.chmod(0o700)
    original_start=Path('/jffs/scripts/services-start').read_bytes()
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
    (root/'webs').mkdir()
    (root/'scripts').mkdir()
    shutil.copyfile('jffs/Module_cloudhelper.asp',str(root/'webs/Module_cloudhelper.asp'))
    shutil.copyfile('jffs/uninstall_cloudhelper.sh',str(root/'scripts/uninstall_cloudhelper.sh'))
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
            assert foreign.read_text(encoding='utf-8')=='retain'
            foreign.unlink()
            for op in ('arbitrary-path','unlocal','unmenu','unweb'):
                assert subprocess.call([plugin,'--runtime-mount',op],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)!=0
            foreign=root/'foreign-menu';foreign.write_text('third party menu')
            run('mount','--bind',str(foreign),str(target))
            assert subprocess.call([plugin,'--runtime-mount','unmenu'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)!=0
            assert target.read_text(encoding='utf-8')=='third party menu'
            run('umount',str(target))
            foreign.unlink()
        run('sh',str(root/'install.sh'))
        assert status()
        assert 'CHVR_JFFS_MENU' in target.read_text(encoding='utf-8')
        assert not (base/'admin.key').exists()
        assert b'CHVR_HTTPD_WRAPPER' in Path('/usr/sbin/httpd').read_bytes()
        if koolcenter:
            assert 'CHVR_SOFTCENTER_ENTRY' in Path('/koolshare/webs/Module_cloudhelper.asp').read_text(encoding='utf-8')
            assert 'softcenter_module_cloudhelper_install=1' in Path('/tmp/chvr-dbus').read_text(encoding='utf-8')
            assert (base/'installer.sh').is_file()
            start_hook=Path('/koolshare/init.d/V99cloudhelper.sh')
            stop_hook=Path('/koolshare/init.d/T99cloudhelper.sh')
            assert run(str(base/'service'),'boot-status').strip()=='koolcenter enabled'
            saved_hook=start_hook.read_bytes()
            start_hook.write_text('foreign hook')
            assert subprocess.call([str(base/'service'),'boot-install'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)!=0
            assert start_hook.read_text(encoding='utf-8')=='foreign hook' and stop_hook.exists()
            start_hook.unlink();start_hook.symlink_to(other)
            assert subprocess.call([str(base/'service'),'boot-install'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)!=0
            assert start_hook.is_symlink() and other.exists()
            start_hook.unlink()
            start_hook.write_bytes(saved_hook)
            start_hook.chmod(0o700)
            run('/bin/sh','/jffs/scripts/services-stop')
            (base/'autostart.disabled').touch()
            run('/bin/sh','/jffs/scripts/services-start')
            assert not Path('/var/run/cloudhelper-router/admin.sock').exists()
            assert Path('/tmp/chvr-other-started').exists()
            (base/'autostart.disabled').unlink()
            run('/bin/sh','/jffs/scripts/services-start')
            for attempt in range(100):
                try:
                    if status():break
                except (OSError,ValueError):pass
                time.sleep(.1)
            else:raise AssertionError('Startup chain failed to restore service')
            # Wait for asynchronous web/menu restoration as well as the socket.
            for attempt in range(150):
                if 'CHVR_JFFS_MENU' in target.read_text(encoding='utf-8'):break
                time.sleep(.1)
            else:raise AssertionError('Startup chain failed to restore menu')
            assert Path('/jffs/scripts/services-start').read_bytes()==original_start
        key=b'legacy retained'
        (base/'admin.key').write_bytes(key)
        prepare('b'*16)
        run('sh',str(root/'install.sh'))
        assert (base/'active').read_text(encoding='utf-8').strip()=='b'*16 and status()
        assert (base/'admin.key').read_bytes()==key
        run('sh',str(root/'install.sh'),'--rollback')
        assert (base/'active').read_text(encoding='utf-8').strip()=='a'*16 and status()
        # Execute the helper from the staged verified payload, as the online
        # worker does. The live service tmpfs must be removable during install.
        prepare('c'*16)
        stage=Path('/tmp/chvr-upgrade.'+secrets.token_hex(3));stage.mkdir(mode=0o700)
        shutil.copytree(str(root),str(stage/'cloudhelper'))
        (stage/'package.tar.gz').write_bytes(b'verified-package-fixture')
        (stage/'job.json').write_text(json.dumps({'sha256':hashlib.sha256((stage/'package.tar.gz').read_bytes()).hexdigest(),'version':'v0.4.72'}))
        run(str(stage/'cloudhelper/payload/cloudhelper-router-plugin'),'--apply-upgrade',str(stage))
        assert not stage.exists()
        assert (base/'active').read_text(encoding='utf-8').strip()=='c'*16 and status()
        assert json.loads(Path('/var/run/cloudhelper-router/upgrade.json').read_text(encoding='utf-8'))['phase']=='complete'
        assert (base/'admin.key').read_bytes()==key
        if koolcenter:run('sh','/koolshare/scripts/uninstall_cloudhelper.sh')
        else:run('sh',str(root/'install.sh'),'--uninstall')
        assert not (base/'active').exists() and not (base/'service').exists()
        assert (base/'admin.key').read_bytes()==key and target.read_bytes()==original
        assert not list(base.glob('**/*.log'))
        if koolcenter:
            assert not Path('/koolshare/webs/Module_cloudhelper.asp').exists()
            assert 'remove softcenter_module_cloudhelper_install' in Path('/tmp/chvr-dbus').read_text(encoding='utf-8')
            assert not start_hook.exists() and not stop_hook.exists() and other.exists()
            assert Path('/jffs/scripts/services-start').read_bytes()==original_start
        assert Path('/usr/local/share').is_symlink()
        assert list(Path('/usr/local').iterdir())==[Path('/usr/local/share')]
    finally:
        if (base/'service').exists():subprocess.call(['sh',str(base/'service'),'stop'])
print('PASS: install, Unix-only management, stock web extension mount, actual stock menu, update, credential retention, rollback and uninstall')
