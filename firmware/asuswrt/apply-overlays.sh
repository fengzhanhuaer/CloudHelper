#!/bin/bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/tools/baseline-env.sh"
expected_sha="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["source"]["archive_sha256"])' "$firmware_root/baseline.lock.json")"
test "$(cat "$task_root/.official-archive.sha256")" = "$expected_sha"
cd "$source_root"
shopt -s nullglob
for patch_file in "$firmware_root"/patches/*.patch; do
    if [[ "$(basename "$patch_file")" == "0002-jffs-autostart.patch" && "${CHVR_AUTOSTART_FIRMWARE:-0}" != 1 ]]; then
        if git apply --reverse --check "$patch_file" 2>/dev/null; then
            echo 'This source tree contains the minimal startup variant; use build-autostart-firmware.sh.' >&2
            exit 1
        fi
        continue
    fi
    if [[ "$(basename "$patch_file")" == "0001-virtual-router-integration.patch" && "${CHVR_FIRMWARE:-0}" != 1 ]]; then
        if git apply --reverse --check "$patch_file" 2>/dev/null; then
            echo 'This source tree contains router integration; use build-router-firmware.sh.' >&2
            exit 1
        fi
        continue
    fi
    if git apply --check "$patch_file"; then
        git apply "$patch_file"
    elif git apply --reverse --check "$patch_file"; then
        echo "Already applied: $(basename "$patch_file")"
    else
        echo "Patch conflicts with official build source: $patch_file" >&2
        exit 1
    fi
done
