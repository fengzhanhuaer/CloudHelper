#!/usr/bin/env python3
"""Package only router runtime assets; never compile the operating system."""
import hashlib
import io
import json
from pathlib import Path
import tarfile

root = Path(__file__).resolve().parents[1]
files = {
    'payload/cloudhelper-router-plugin': root / 'bin/arm/cloudhelper-router-plugin',
    'payload/service': root / 'jffs/service',
    'payload/httpd-bridge.so': root / 'bin/arm/httpd-bridge.so',
    'payload/web/VirtualRouter.asp': root / 'web/VirtualRouter.asp',
    'install.sh': root / 'jffs/install.sh',
}
data = {name: path.read_bytes() for name, path in files.items()}
binary = data['payload/cloudhelper-router-plugin']
if binary[:4] != b'\x7fELF' or binary[4:6] != b'\x01\x01' or int.from_bytes(binary[18:20], 'little') != 40:
    raise SystemExit('Require a little-endian 32-bit ARM ELF')
validation=json.loads((root/'artifacts/stock-runtime-validation.json').read_text(encoding='utf-8'))
if validation.get('result')!='PASS' or validation.get('binary_sha256')!=hashlib.sha256(binary).hexdigest():
    raise SystemExit('Re-run stock runtime verification for this exact binary before packaging')
if not validation.get('runtime_initialization_verified') or validation.get('verifier_sha256')!=hashlib.sha256((root/'tools/check-stock-runtime.py').read_bytes()).hexdigest():
    raise SystemExit('Re-run the current stock runtime initialization verifier')
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
    'product': 'asus_56uv2', 'release_id': release, 'deployment': 'stock-asuswrt-jffs',
    'language': 'C', 'transport': 'TLS/WebSocket', 'management': 'stock ASUS authenticated menu and management port',
    'boot_persistence': 'automatic with CloudHelper minimal JFFS-start firmware; manual SSH bootstrap on unmodified stock firmware',
    'stock_runtime_abi': validation,
    'files': {name: {'bytes': len(body), 'sha256': hashlib.sha256(body).hexdigest()} for name, body in data.items()},
    'uncompressed_bytes': sum(map(len, data.values())),
}
data['manifest.json'] = (json.dumps(report, indent=2) + '\n').encode()
out = root / 'artifacts/jffs'
out.mkdir(parents=True, exist_ok=True)
target = out / ('cloudhelper-asus56uv2-jffs-' + release + '.tar.gz')
with tarfile.open(str(target), 'w:gz') as archive:
    for name, body in sorted(data.items()):
        info = tarfile.TarInfo('cloudhelper-router/' + name)
        info.size, info.mtime = len(body), 0
        info.mode = 0o700 if name in ('install.sh', 'payload/service', 'payload/cloudhelper-router-plugin') else 0o600
        archive.addfile(info, io.BytesIO(body))
report['package_bytes'] = target.stat().st_size
report['sha256'] = hashlib.sha256(target.read_bytes()).hexdigest()
(out / (target.name + '.json')).write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
print(json.dumps({'package': str(target), **report}, indent=2))
