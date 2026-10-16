#!/bin/bash
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/tools/baseline-env.sh"
bash "$firmware_root/apply-overlays.sh"
python3 "$firmware_root/tools/clean-build-cores.py" "$source_root"
attempt_log="$task_root/logs/baseline-build-$(date -u +%Y%m%dT%H%M%SZ).log"
auxiliary_root="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["auxiliary_toolchains"]["linux_directory"])' "$firmware_root/baseline.lock.json")"
docker run --rm --name cloudhelper-asuswrt-official-baseline \
    --cpus 6 --memory 12g --pids-limit 8192 \
    --env GIT_CEILING_DIRECTORIES=/workspace \
    --mount "type=bind,src=$task_root,dst=/workspace" \
    --mount "type=bind,src=$task_root/opt-toolchains,dst=/opt/toolchains,readonly" \
    --mount "type=bind,src=$task_root/projects,dst=/projects,readonly" \
    --mount "type=bind,src=$auxiliary_root,dst=/auxiliary-toolchains,readonly" \
    --workdir "/workspace/$source_relative/$target_relative" \
    cloudhelper-asuswrt-build:ubuntu18 \
    bash -lc 'set -e; ulimit -c 0; test "$(id -u)" -ne 0;
      sdk=/opt/toolchains/crosstools-arm-gcc-5.5-linux-4.1-glibc-2.26-binutils-2.28.1;
      export LD_LIBRARY_PATH="$sdk/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}";
      "$sdk/usr/bin/arm-buildroot-linux-gnueabi-gcc" --version;
      make rt-ax55 PARALLEL_BUILD=-j6' \
    2>&1 | tee "$task_root/logs/baseline-build.log" "$attempt_log"
bash "$firmware_root/collect-baseline.sh"
