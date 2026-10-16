#!/usr/bin/env python3
"""Stage the verified router payload for the vendor rootfs install target."""
import argparse
import hashlib
import json
import shutil
from pathlib import Path


def stage(repo, source):
    module = repo / "router_plugin"
    firmware = repo / "firmware/asuswrt"
    target = source / "release/src/router/cloudhelper-router"
    inputs = {
        "cloudhelper-router-plugin": module / "bin/arm/cloudhelper-router-plugin",
        "cloudhelper-router-service": firmware / "overlay/usr/sbin/cloudhelper-router-service",
        "VirtualRouter.asp": module / "web/VirtualRouter.asp",
    }
    target.mkdir(parents=True, exist_ok=True)
    payload = {}
    for name, path in inputs.items():
        data = path.read_bytes()
        if not data:
            raise ValueError("empty input: " + str(path))
        shutil.copyfile(str(path), str(target / name))
        payload[name] = {"sha256": hashlib.sha256(data).hexdigest(), "bytes": len(data)}
    digest = hashlib.sha256()
    sources = sorted(list((module / "src").glob("*.c")) + list((module / "include").glob("*.h")) +
                     [module / "Makefile", inputs["cloudhelper-router-service"], inputs["VirtualRouter.asp"],
                      firmware / "patches/0001-virtual-router-integration.patch"])
    for path in sources:
        digest.update(str(path.relative_to(repo)).replace("\\", "/").encode() + b"\0" + path.read_bytes())
    manifest = {"product": "asus_56uv2", "language": "C", "transport": "TLS/WebSocket",
                "source_sha256": digest.hexdigest(), "files": payload,
                "baseline_archive_sha256": json.loads((firmware / "baseline.lock.json").read_text())["source"]["archive_sha256"]}
    (target / "build-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print("Staged router payload " + digest.hexdigest())


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("repo", type=Path)
    p.add_argument("source", type=Path)
    a = p.parse_args()
    stage(a.repo, a.source)
