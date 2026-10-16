#!/usr/bin/env python3
"""Install a verified JFFS bundle using the authorized router SSH connection."""
import argparse
import hashlib
import http.client
import json
from pathlib import Path
import secrets
import shlex
from stock_web import StockWeb, settings_credentials
import subprocess
import tarfile

p=argparse.ArgumentParser()
p.add_argument('package',type=Path)
p.add_argument('--ssh',default='localrouter')
p.add_argument('--report',type=Path)
p.add_argument('--settings',type=Path,help='Private ASUS settings export for authenticated UI checks')
args=p.parse_args()
allowed={'install.sh','checksums','release-id','manifest.json','payload/cloudhelper-router-plugin',
         'payload/service','payload/httpd-bridge.so','payload/web/VirtualRouter.asp'}
with tarfile.open(str(args.package),'r:gz') as archive:
    members=archive.getmembers()
    assert len(members)==len(allowed)
    assert {m.name for m in members}=={'cloudhelper-router/'+s for s in allowed}
    assert all(m.isfile() and m.size<256*1024 for m in members)
    content={m.name.split('/',1)[1]:archive.extractfile(m).read() for m in members}
    manifest=json.loads(content['manifest.json'])
    for name,record in manifest['files'].items():
        assert len(content[name])==record['bytes']
        assert hashlib.sha256(content[name]).hexdigest()==record['sha256']
    release=content['release-id'].decode().strip()
    assert len(release)==16 and all(c in '0123456789abcdef' for c in release)

ssh=['ssh','-o','BatchMode=yes','-o','ConnectTimeout=8',args.ssh]
def remote(command,data=None,timeout=30):
    result=subprocess.run(ssh+[command],input=data,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=timeout)
    if result.returncode:
        raise RuntimeError('Router command failed: '+result.stdout.decode(errors='replace')+result.stderr.decode(errors='replace'))
    return result.stdout

identity=remote("nvram get productid; nvram get odmpid; uname -m; nvram get extendno; awk '$1==\"Uid:\" {print $2}' /proc/self/status").decode().splitlines()
assert identity[:3]==['RT-AX55','RT-AX56U_V2','armv7l'] and identity[4]=='0'
directory='/tmp/chvr-jffs-install.'+secrets.token_hex(8)
q=shlex.quote
remote('umask 077 && mkdir '+q(directory))
try:
    data=args.package.read_bytes()
    remote('cat > '+q(directory+'/bundle.tar.gz'),data)
    assert hashlib.sha256(remote('cat '+q(directory+'/bundle.tar.gz'))).digest()==hashlib.sha256(data).digest()
    remote('tar xzf '+q(directory+'/bundle.tar.gz')+' -C '+q(directory))
    # Some vendor PATHs contain a memory-debug utility also named "sh".
    output=remote('/bin/sh '+q(directory+'/cloudhelper-router/install.sh'),timeout=60)
    print(output.decode(errors='replace'),end='')
    base='/jffs/cloudhelper-router'
    assert remote('cat '+base+'/active').decode().strip()==release
    for name in ('cloudhelper-router-plugin','service','httpd-bridge.so','web/VirtualRouter.asp'):
        assert hashlib.sha256(remote('cat '+base+'/releases/'+release+'/'+name)).digest()==hashlib.sha256(content['payload/'+name]).digest()
    web=StockWeb(remote,settings_credentials(args.settings) if args.settings else None)
    status=web.api({'action':'status'})
    assert status['ok']
    code,headers,page=web.request('/user/cloudhelper-router/VirtualRouter.asp')
    assert code==200 and '虚拟路由'.encode() in page and b'mainMenu' in page
    assert not web.request('/user/cloudhelper-router/api.asp',b'{"action":"status"}',authenticated=False)[2].startswith(b'{"ok":true')
    # Report only public state; neither credential nor CSRF token is persisted.
    report={'result':'PASS','release_id':release,'version':identity[3],
        'package_bytes':len(data),'package_sha256':hashlib.sha256(data).hexdigest(),
        'installed_payload_bytes':sum(len(content['payload/'+s]) for s in ('cloudhelper-router-plugin','service','httpd-bridge.so','web/VirtualRouter.asp')),
        'payload_readback_verified':True,'stock_web_auth_verified':True,
        'management_url':web.origin+'/user/cloudhelper-router/VirtualRouter.asp','business_enabled':status['config']['enabled'],
        'controller_configured':bool(status['config']['controller']),'node_key_configured':status['key_set'],
        'service':remote(base+'/service status').decode().strip(),
        'jffs_usage':remote('du -sk '+base+'; df -k /jffs').decode(),
        'menu_mounted':b'CHVR_JFFS_MENU' in remote('cat /www/require/modules/menuTree.js'),
        'boot_persistence':'automatic via minimal firmware JFFS startup hook'
            if remote('test -x /rom/etc/cloudhelper-jffs-start && echo yes || true').strip()==b'yes'
            else 'manual SSH start after stock firmware reboot'}
    print(json.dumps(report,indent=2))
    if args.report:
        args.report.parent.mkdir(parents=True,exist_ok=True)
        args.report.write_text(json.dumps(report,indent=2)+'\n',encoding='utf-8')
finally:
    paths=[directory+'/cloudhelper-router/'+s for s in sorted(allowed)]
    remote('rm -f '+' '.join(q(s) for s in paths+[directory+'/bundle.tar.gz'])+
           ' && rmdir '+' '.join(q(directory+'/cloudhelper-router/'+s) for s in ('payload/web','payload',''))+
           ' '+q(directory))
