#!/bin/bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/tools/baseline-env.sh"
image_root="$source_root/$target_relative/targets/947622GW"
mkdir -p "$task_root/artifacts"
shopt -s nullglob
images=("$image_root"/*.w)
test "${#images[@]}" -gt 0
for source_image in "${images[@]}"; do
    cp -p "$source_image" "$task_root/artifacts/$(basename "$source_image")"
done
sha256sum "$task_root/artifacts"/*.w > "$task_root/artifacts/baseline-images.sha256"
cat "$task_root/artifacts/baseline-images.sha256"
