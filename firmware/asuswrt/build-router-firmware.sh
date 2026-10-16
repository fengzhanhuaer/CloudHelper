#!/bin/bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/tools/baseline-env.sh"
repo_root="$(cd "$firmware_root/../.." && pwd)"
bash "$repo_root/router_plugin/tools/build-arm.sh"
python3 "$firmware_root/tools/stage-router.py" "$repo_root" "$source_root"
CHVR_FIRMWARE=1 bash "$firmware_root/build-baseline.sh"
python3 "$firmware_root/tools/verify-baseline.py" "$task_root"
output="$firmware_root/artifacts/router"
mkdir -p "$output"
python3 - "$task_root" "$output" "$source_root" <<'PY'
import hashlib,json,shutil,sys
from pathlib import Path
task,output,source=map(Path,sys.argv[1:])
manifest=json.loads((source/'release/src/router/cloudhelper-router/build-manifest.json').read_text())
images=[p for p in (task/'artifacts').glob('*_puresqubi.w') if not p.name.endswith('_cferom_puresqubi.w')]
if len(images)!=1: raise SystemExit('Expected exactly one verified no-CFE firmware image')
root=source/'release/src-rt-5.02axhnd.675x/targets/947622GW/fs'
for name,relative in [('cloudhelper-router-plugin','usr/sbin/cloudhelper-router-plugin'),('cloudhelper-router-service','usr/sbin/cloudhelper-router-service'),('VirtualRouter.asp','www/VirtualRouter.asp')]:
    if hashlib.sha256((root/relative).read_bytes()).hexdigest()!=manifest['files'][name]['sha256']:
        raise SystemExit('Rootfs payload mismatch: '+relative)
if json.loads((root/'rom/etc/cloudhelper-router-build.json').read_text())!=manifest:
    raise SystemExit('Rootfs manifest mismatch')
target=output/('CloudHelper_RT-AX56U_V2_'+manifest['source_sha256'][:12]+'_puresqubi.w')
shutil.copyfile(str(images[0]),str(target))
manifest['image']={'name':target.name,'bytes':target.stat().st_size,'sha256':hashlib.sha256(target.read_bytes()).hexdigest()}
(output/(target.name+'.json')).write_text(json.dumps(manifest,indent=2)+'\n')
print(target)
PY
