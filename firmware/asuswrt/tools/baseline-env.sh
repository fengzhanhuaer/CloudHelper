#!/bin/bash
# Paths are selected by the official source contract.
firmware_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
task_root="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["source"]["linux_task_root"])' "$firmware_root/baseline.lock.json")"
source_relative="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["source"]["linux_source_relative_path"])' "$firmware_root/baseline.lock.json")"
source_root="$task_root/$source_relative"
target_relative="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["target"]["directory"])' "$firmware_root/baseline.lock.json")"
sdk_relative="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["toolchains"]["relative_path"])' "$firmware_root/baseline.lock.json")"
