#!/usr/bin/env python3
"""Package only router runtime assets; never compile the operating system."""
import hashlib
import argparse
import io
import json
from pathlib import Path
import tarfile

root = Path(__file__).resolve().parents[1]
p=argparse.ArgumentParser()
p.add_argument('--profile',choices=('ax56uv2','be86u'),default='ax56uv2')
args=p.parse_args()
files = {
    'payload/cloudhelper-router-plugin': root / 'bin/arm/cloudhelper-router-plugin',
    'payload/service': root / 'jffs/service',
    'payload/httpd-bridge.so': root / 'bin/arm/httpd-bridge.so',
    'payload/web/VirtualRouter.asp': root / 'web/VirtualRouter.asp',
    'install.sh': root / 'jffs/install.sh',
    'webs/Module_cloudhelper.asp': root / 'jffs/Module_cloudhelper.asp',
    'scripts/uninstall_cloudhelper.sh': root / 'jffs/uninstall_cloudhelper.sh',
}
data = {name: path.read_bytes() for name, path in files.items()}
data['.valid']=b'hnd\n'
data['device-profile']=(args.profile+'\n').encode()
binary = data['payload/cloudhelper-router-plugin']
if binary[:4] != b'\x7fELF' or binary[4:6] != b'\x01\x01' or int.from_bytes(binary[18:20], 'little') != 40:
    raise SystemExit('Require a little-endian 32-bit ARM ELF')
validation_path='stock-runtime-validation.json' if args.profile=='ax56uv2' else 'be86u-runtime-validation.json'
validation=json.loads((root/'artifacts'/validation_path).read_text(encoding='utf-8'))
extra_validation=[]
if args.profile=='ax56uv2':
    extra_validation.append(json.loads((root/'artifacts/ax56uv2-koolshare-runtime-validation.json').read_text(encoding='utf-8')))
    if extra_validation[0].get('device_profile')!='ax56uv2-koolshare':
        raise SystemExit('Require current AX56U V2 KoolShare runtime verification')
if args.profile=='be86u':
    extra_validation.append(json.loads((root/'artifacts/be86u-koolcenter-runtime-validation.json').read_text(encoding='utf-8')))
    if extra_validation[0].get('device_profile')!='be86u-koolcenter' or not extra_validation[0].get('hidden_httpd_layout_verified'):
        raise SystemExit('Require current KoolCenter runtime and hidden handler verification')
if args.profile=='be86u' and (validation.get('device_profile')!='be86u' or not validation.get('hidden_httpd_layout_verified')):
    raise SystemExit('Require exact BE86U stock runtime and hidden handler verification')
for record in extra_validation:
    for key in ('result','binary_sha256','httpd_bridge_sha256','verifier_sha256','profile_verifier_sha256','profile_header_sha256','shell_sha256','version','runtime_initialization_verified'):
        if record.get(key)!=validation.get(key):raise SystemExit('Re-run all firmware runtime profiles for the same payloads')
if validation.get('result')!='PASS' or validation.get('binary_sha256')!=hashlib.sha256(binary).hexdigest():
    raise SystemExit('Re-run stock runtime verification for this exact binary before packaging')
if not validation.get('runtime_initialization_verified') or validation.get('verifier_sha256')!=hashlib.sha256((root/'tools/check-stock-runtime.py').read_bytes()).hexdigest():
    raise SystemExit('Re-run the current stock runtime initialization verifier')
if (validation.get('profile_verifier_sha256')!=hashlib.sha256((root/'tools/httpd_profile.py').read_bytes()).hexdigest() or
    validation.get('profile_header_sha256')!=hashlib.sha256((root/'include/httpd_profile.h').read_bytes()).hexdigest()):
    raise SystemExit('Re-run exact HTTPD profile verification')
if validation.get('shell_sha256')!={name:hashlib.sha256((root/'jffs'/name).read_bytes()).hexdigest() for name in ('service','install.sh')}:
    raise SystemExit('Re-run stock shell verification for the exact installer/service')
if validation.get('httpd_bridge_sha256')!=hashlib.sha256(data['payload/httpd-bridge.so']).hexdigest():
    raise SystemExit('Re-run stock bridge verification before packaging')
for name in ('install.sh', 'payload/service'):
    if b'\r' in data[name]:
        raise SystemExit('Shell asset must use LF: ' + name)
checksums = ''.join(hashlib.sha256(body).hexdigest() + ' ' + name + '\n'
                    for name, body in sorted(data.items()) if name.startswith('payload/'))
release = hashlib.sha256(b''.join(name.encode() + b'\0' + body for name, body in sorted(data.items()))).hexdigest()[:16]
data['checksums'] = checksums.encode()
data['release-id'] = (release + '\n').encode()
report = {
    'product': 'asus_56uv2' if args.profile=='ax56uv2' else 'asus_be86u',
    'device_profile':args.profile,'release_id': release, 'deployment': 'stock-asuswrt-jffs',
    'version':validation['version'],
    'language': 'C', 'transport': 'TLS/WebSocket', 'management': 'stock ASUS authenticated menu and management port',
    'boot_persistence': ('automatic on verified 39125_koolcenter startup hooks; manual SSH bootstrap on official 39436' if args.profile=='be86u' else
        'automatic on verified 49559_koolshare or CloudHelper minimal JFFS-start firmware; manual SSH bootstrap on unmodified stock firmware'),
    'stock_runtime_abi': validation,
    'additional_runtime_abi':extra_validation,
    'files': {name: {'bytes': len(body), 'sha256': hashlib.sha256(body).hexdigest()} for name, body in data.items()},
    'uncompressed_bytes': sum(map(len, data.values())),
}
data['manifest.json'] = (json.dumps(report, indent=2) + '\n').encode()
out = root / 'artifacts/jffs'
out.mkdir(parents=True, exist_ok=True)
target = out / ('cloudhelper-asus'+('56uv2' if args.profile=='ax56uv2' else 'be86u')+'-jffs.tar.gz')
with tarfile.open(str(target), 'w:gz') as archive:
    for name, body in sorted(data.items()):
        info = tarfile.TarInfo('cloudhelper/' + name)
        info.size, info.mtime = len(body), 0
        info.mode = 0o700 if name in ('install.sh', 'payload/service', 'payload/cloudhelper-router-plugin', 'scripts/uninstall_cloudhelper.sh') else 0o600
        archive.addfile(info, io.BytesIO(body))
report['package_bytes'] = target.stat().st_size
report['sha256'] = hashlib.sha256(target.read_bytes()).hexdigest()
(out / (target.name + '.json')).write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
print(json.dumps({'package': str(target), **report}, indent=2))
