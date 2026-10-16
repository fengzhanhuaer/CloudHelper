#!/bin/bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/tools/baseline-env.sh"
for tool in docker git python3 qemu-arm readelf unsquashfs; do
    command -v "$tool" >/dev/null || { echo "Missing build/verification prerequisite: $tool" >&2; exit 1; }
done
python3 "$firmware_root/tools/prepare-autostart.py"
CHVR_AUTOSTART_FIRMWARE=1 bash "$firmware_root/build-baseline.sh"
python3 "$firmware_root/tools/verify-baseline.py" "$task_root"
python3 "$firmware_root/tools/verify-autostart.py" "$task_root"
