#!/bin/bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/baseline-env.sh"
sdk_root="$source_root/$sdk_relative"
mkdir -p "$task_root/opt-toolchains" "$task_root/sdk-compat" "$task_root/projects/bca/tools/linux/bin"
for sdk in "$sdk_root"/crosstools-*; do
    test -d "$sdk"
    name="$(basename "$sdk")"
    compatibility_dir="$task_root/sdk-compat/$name"
    mkdir -p "$compatibility_dir"
    # Some ASUS makefiles expect usr/bin/ instead of the official SDK bin/.
    # Map both layouts outside the archived source; retain original SDK files.
    for entry in "$sdk"/*; do
        ln -sfn "/workspace/$source_relative/$sdk_relative/$name/$(basename "$entry")" "$compatibility_dir/$(basename "$entry")"
    done
    if [ ! -e "$sdk/usr" ]; then
        ln -sfn . "$compatibility_dir/usr"
    fi
    ln -sfn "/workspace/sdk-compat/$name" "$task_root/opt-toolchains/$name"
done
auxiliary_root="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["auxiliary_toolchains"]["linux_directory"])' "$firmware_root/baseline.lock.json")"
test "$(cat "$auxiliary_root/.archive.sha256")" = "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["auxiliary_toolchains"]["archive_sha256"])' "$firmware_root/baseline.lock.json")"
test -d "$auxiliary_root/hndtools-armeabi-2013.11"
ln -sfn /auxiliary-toolchains/hndtools-armeabi-2013.11 "$task_root/projects/bca/tools/linux/hndtools-armeabi-2013.11"
ln -sfn /auxiliary-toolchains/fwtag.ini "$task_root/projects/bca/tools/linux/bin/fwtag.ini"
ln -sfn /projects/bca "$task_root/projects/hnd"
