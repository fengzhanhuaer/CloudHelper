"""Real bind mounts and stock DNS-file lifecycle in an isolated container only."""
import os
from pathlib import Path
import shutil
import subprocess
import sys

if os.environ.get('CHVR_DISPOSABLE_TEST')!='1' or not Path('/.dockerenv').exists():
    raise SystemExit('Disposable privileged container required; never host networking')

def run(*args, **kwargs):
    return subprocess.check_output(args,stderr=subprocess.STDOUT,universal_newlines=True,**kwargs)

fixture=Path('/tmp/chvr-bin');fixture.mkdir(exist_ok=True)
(fixture/'killall').write_text('#!/bin/sh\necho signal >> /tmp/chvr-signals\n')
(fixture/'nvram').write_text('#!/bin/sh\ncase "$2" in lan_ipaddr) echo 192.168.52.1;; esac\n')
for file in fixture.iterdir():file.chmod(0o700)
os.environ['PATH']=str(fixture)+':'+os.environ['PATH']
helper=str(Path(sys.argv[1]).resolve())
servers=Path('/tmp/resolv.dnsmasq');backup=Path('/tmp/cloudhelper-dns.original')
original='server=192.0.2.1\nserver=/internal.test/192.0.2.2\n'
servers.write_text(original)
run(helper,'--dns','1')
assert '127.0.0.1#1053' in servers.read_text() and backup.read_text()==original
assert servers.stat().st_mode & 0o777 == 0o644
assert backup.stat().st_mode & 0o777 == 0o600
signals=Path('/tmp/chvr-signals').read_text()
run(helper,'--dns','1')
assert Path('/tmp/chvr-signals').read_text()==signals
servers.chmod(0o600)  # repair an older installed release's unreadable file
run(helper,'--dns','1')
assert servers.stat().st_mode & 0o777 == 0o644
assert Path('/tmp/chvr-signals').read_text()!=signals
updated='server=203.0.113.1\n'
servers.write_text(updated)  # vendor WAN/DHCP regeneration
run(helper,'--dns','1')
assert backup.read_text()==updated
run(helper,'--dns','0')
assert servers.read_text()==updated and not backup.exists()
assert servers.stat().st_mode & 0o777 == 0o644
run(helper,'--dns','0')
run(helper,'--dns','1')
servers.write_text(original)
run(helper,'--dns','0')
assert servers.read_text()==original and not backup.exists()
print('PASS: stock DNS servers-file idempotence, latest vendor updates and cleanup')

release=Path('/jffs/cloudhelper-router/releases/0123456789abcdef');release.mkdir(parents=True,exist_ok=True)
shutil.copyfile('jffs/service',str(release/'service'))
target=Path('/www/require/modules/menuTree.js');target.parent.mkdir(parents=True,exist_ok=True)
source='define(function(){\nvar menuTree={list:[{index:"menu_NekworkTool",tab:[]}],exclude:{}};\nreturn menuTree;\n});\n'
target.write_text(source)
service=['sh',str(release/'service')]
try:
    run(*service+['menu'])
    assert 'CHVR_JFFS_MENU' in target.read_text(encoding='utf-8') and 'user/cloudhelper-router/VirtualRouter.asp' in target.read_text(encoding='utf-8')
    run(*service+['menu'])
    assert target.read_text(encoding='utf-8').count('CHVR_JFFS_MENU')==1
    run(*service+['unmenu'])
    assert target.read_text()==source
    target.write_text('unrecognized stock menu')
    assert subprocess.call(service+['menu'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)!=0
    assert target.read_text()=='unrecognized stock menu'
finally:
    subprocess.call(service+['unmenu'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
print('PASS: real menu bind mount, repeat/restore and reject unknown stock structure')
