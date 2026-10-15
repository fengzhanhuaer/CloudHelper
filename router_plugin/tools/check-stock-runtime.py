#!/usr/bin/env python3
"""Run the ARM payload against libraries extracted from the vendor firmware."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import shutil

p=argparse.ArgumentParser()
p.add_argument('rootfs',type=Path)
p.add_argument('--readelf',default='readelf')
args=p.parse_args()
module=Path(__file__).resolve().parents[1]
binary=module/'bin/arm/cloudhelper-router-plugin'
bridge=module/'bin/arm/httpd-bridge.so'
out=module/'artifacts/stock-runtime-validation.json'
if out.exists():out.unlink()
subprocess.check_call([sys.executable,str(module/'tools/check-elf-deps.py'),str(binary),str(args.rootfs),'--readelf',args.readelf])
subprocess.check_call([sys.executable,str(module/'tools/check-elf-deps.py'),str(bridge),str(args.rootfs),'--readelf',args.readelf])
prefix=['qemu-arm','-L',str(args.rootfs),'-E','LD_LIBRARY_PATH=/usr/lib:/lib']
subprocess.check_call(prefix+[str(binary),'--version'])
subprocess.check_call(prefix+[str(module/'bin/arm/test-core')])
result=subprocess.check_output(prefix+['-E','LD_PRELOAD='+str(bridge),str(module/'bin/arm/httpd-fixture'),'--check-bridge'],universal_newlines=True)
if result.strip()!='stock handler extension active':raise SystemExit('ARM httpd bridge failed')
symbols=subprocess.check_output([args.readelf,'--dyn-syms','--wide',str(args.rootfs/'usr/sbin/httpd')],universal_newlines=True)
for name in ('mime_handlers','do_ssl','ssl_stream_fd'):
    if not any(line.split()[-1:]==[name] and ' OBJECT ' in line for line in symbols.splitlines()):
        raise SystemExit('Stock httpd export absent: '+name)
with tempfile.TemporaryDirectory(prefix='chvr-check-',dir='/usr/local') as directory:
    # Stock SSL explicitly supports executables below /usr. Confirm the real
    # runtime initialization completes; --version alone does not exercise it.
    runtime=Path(directory)/'cloudhelper-router-plugin'
    shutil.copyfile(str(binary),str(runtime));runtime.chmod(0o700)
    output=subprocess.check_output(prefix+[str(runtime),'--config',str(Path(directory)/'absent.json'),'--check-config'],universal_newlines=True)
    if output.strip()!='configuration valid':
        raise SystemExit('Stock runtime exited without completing initialization')
for name in ('service','install.sh'):
    subprocess.check_call(prefix+[str(args.rootfs/'bin/busybox'),'sh','-n',str(module/'jffs'/name)])
report={
    'result':'PASS','baseline':'ASUS vendor 3.0.0.4.386_52332-gfbf3b9c',
    'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),
    'httpd_bridge_sha256':hashlib.sha256(bridge.read_bytes()).hexdigest(),
    'stock_httpd_sha256':hashlib.sha256((args.rootfs/'usr/sbin/httpd').read_bytes()).hexdigest(),
    'stock_busybox_sha256':hashlib.sha256((args.rootfs/'bin/busybox').read_bytes()).hexdigest(),
    'runtime_initialization_verified':True,
    'verifier_sha256':hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
    'shell_sha256':{name:hashlib.sha256((module/'jffs'/name).read_bytes()).hexdigest() for name in ('service','install.sh')},
    'scope':'QEMU loader, eager symbols, core, actual SSL initialization from RAM /usr path, BusyBox syntax; not device business validation',
}
out.parent.mkdir(parents=True,exist_ok=True)
out.write_text(json.dumps(report,indent=2)+'\n',encoding='utf-8')
print(json.dumps(report,indent=2))
