#!/bin/bash
set -euo pipefail
script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$script_root/tools/baseline-env.sh"
archive_name="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["source"]["archive_name"])' "$script_root/baseline.lock.json")"
expected_sha="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["source"]["archive_sha256"])' "$script_root/baseline.lock.json")"
archive="$script_root/upstream/official/$archive_name"
test -s "$archive"
actual_sha="$(sha256sum "$archive" | cut -d ' ' -f1)"
test "$actual_sha" = "$expected_sha" || { echo 'Official archive checksum mismatch' >&2; exit 1; }
mkdir -p "$task_root/source" "$task_root/logs" "$task_root/artifacts"
if [ ! -f "$source_root/release/src/router/extendno.conf" ]; then
    tar -xzf "$archive" --no-same-owner -C "$task_root/source"
fi
test "$(cat "$source_root/release/src/router/extendno.conf")" = 'EXTENDNO=53297-gee411da'
test -f "$source_root/$target_relative/targets/947622GW/947622GW.RT-AX55"
printf '%s\n' "$expected_sha" > "$task_root/.official-archive.sha256"
python3 "$script_root/tools/prepare-auxiliary-sdk.py"
bash "$script_root/tools/map-toolchains.sh"
echo 'Verified official source input and prepared isolated Linux build paths.'
