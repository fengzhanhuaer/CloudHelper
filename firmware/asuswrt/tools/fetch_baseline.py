"""Fetch pinned legacy HND build helpers in restartable Git-object batches.

Run with Windows Git when WSL cannot reach GitHub. Other hardware platforms and
other compiler versions are omitted from the checkout, not from the target's
module configuration. Every file comes from the pinned upstream Git tree.
"""

import argparse
import io
import json
import os
import shutil
import subprocess
import tarfile
import time
from pathlib import Path


SPECS = {
    "toolchains": {
        "commit": "7710a1e09d994598ac6c2db8ab16dc54ca5aed3d",
        "include": ["crosstools-arm-gcc-5.5-linux-4.1-glibc-2.26-binutils-2.28.1/",
                    "crosstools-aarch64-gcc-5.5-linux-4.1-glibc-2.26-binutils-2.28.1/",
                    "hndtools-armeabi-2013.11/"],
    },
}


def run_git(repo, *args, **kwargs):
    return subprocess.run(["git", "--git-dir", str(repo), *args], check=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs).stdout


def local_blobs(repo):
    output = run_git(repo, "cat-file", "--batch-check", "--batch-all-objects")
    return {line.split()[0].decode() for line in output.splitlines()
            if line.split()[1] == b"blob"}


def existing_blobs(repo, objects):
    result = subprocess.run(["git", "--git-dir", str(repo), "cat-file", "--batch-check"],
                            input=("\n".join(objects) + "\n").encode(),
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True,
                            env=dict(os.environ, GIT_NO_LAZY_FETCH="1"))
    return {line.split()[0].decode() for line in result.stdout.splitlines()
            if len(line.split()) == 3 and line.split()[1] == b"blob"}


def fetch_group(repo, group, progress):
    # Fetching blob refspecs can report 'bad revision' after receiving them.
    # Verify object presence instead of interpreting that exit code as success.
    for attempt in range(3):
        command = [
            "git", "--git-dir", str(repo),
            "-c", "http.version=HTTP/1.1", "-c", "http.lowSpeedLimit=1000",
            "-c", "http.lowSpeedTime=45", "-c", "gc.auto=0",
            "-c", "fetch.negotiationAlgorithm=noop",
            "fetch", "--filter=blob:none", "--no-tags", "--no-write-fetch-head",
            "--recurse-submodules=no", "origin", *group,
        ]
        process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            _, errors = process.communicate(timeout=600)
        except subprocess.TimeoutExpired:
            if shutil.which("taskkill.exe"):
                subprocess.run(["taskkill.exe", "/PID", str(process.pid), "/T", "/F"],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            else:
                process.kill()
            _, errors = process.communicate()
        present = existing_blobs(repo, group)
        progress.update(present)
        remaining = [oid for oid in group if oid not in present]
        if not remaining:
            return
        with repo.with_suffix(".fetch-errors.log").open("ab") as log:
            log.write(f"Attempt {attempt + 1}; {len(group)} objects\n".encode() + errors + b"\n")
        print(f"Retry {attempt + 1}: {len(remaining)} objects still missing; "
              + errors.decode(errors="replace")[-200:].strip(), flush=True)
        group = remaining
        time.sleep(2)
    if len(group) > 1:
        midpoint = len(group) // 2
        fetch_group(repo, group[:midpoint], progress)
        fetch_group(repo, group[midpoint:], progress)
    else:
        raise RuntimeError("Unable to fetch Git object " + group[0])


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("kind", choices=SPECS)
    parser.add_argument("repo", type=Path)
    parser.add_argument("archive", type=Path)
    parser.add_argument("--batch-size", type=int, default=512)
    parser.add_argument("--shard-count", type=int, default=1)
    parser.add_argument("--shard-index", type=int, default=0)
    parser.add_argument("--fetch-only", action="store_true")
    args = parser.parse_args()
    if not 1 <= args.batch_size <= 700:
        parser.error("batch size must be between 1 and 700 (Windows command limit)")
    if not 0 <= args.shard_index < args.shard_count:
        parser.error("invalid shard index/count")
    if args.shard_count > 1 and not args.fetch_only:
        parser.error("sharded downloads require --fetch-only")
    spec = SPECS[args.kind]
    actual = run_git(args.repo, "rev-parse", "HEAD").decode().strip()
    if actual != spec["commit"]:
        raise ValueError("Unexpected upstream commit: " + actual)
    rows = run_git(args.repo, "ls-tree", "-r", "-z", "HEAD").split(b"\0")
    entries = []
    for row in rows:
        if not row:
            continue
        metadata, raw_path = row.split(b"\t", 1)
        mode, kind, oid = metadata.decode().split()
        path = raw_path.decode()
        if any(path.startswith(prefix) for prefix in spec.get("exclude", [])):
            continue
        if "include" in spec and "/" in path and not any(
                path.startswith(prefix) for prefix in spec["include"]):
            continue
        if kind != "blob":
            raise ValueError("Unsupported Git entry: " + path)
        entries.append((mode, oid, path))
    args.archive.parent.mkdir(parents=True, exist_ok=True)
    inventory = args.archive.with_suffix(".inventory.json")
    inventory.write_text(json.dumps({"commit": actual, "kind": args.kind,
                                     "entries": entries}, ensure_ascii=False), encoding="utf-8")
    unique = list(dict.fromkeys(oid for _, oid, _ in entries))
    if args.shard_count > 1:
        unique = unique[args.shard_index::args.shard_count]
    present = local_blobs(args.repo)
    missing = [oid for oid in unique if oid not in present]
    print(f"{args.kind}: {len(entries)} files; {len(missing)} missing unique blobs", flush=True)
    # Small packs allow completed batches to survive an interrupted connection.
    for offset in range(0, len(missing), args.batch_size):
        fetch_group(args.repo, missing[offset:offset + args.batch_size], present)
        print(f"{args.kind}: batch {offset // args.batch_size + 1}; "
              f"{sum(oid in present for oid in unique)}/{len(unique)} objects ready", flush=True)
    if args.fetch_only:
        print("All assigned pinned objects downloaded", flush=True)
        return
    partial = args.archive.with_name(args.archive.name + ".partial")
    process = subprocess.Popen(["git", "--git-dir", str(args.repo), "cat-file", "--batch"],
                               stdin=subprocess.PIPE, stdout=subprocess.PIPE)
    try:
        with tarfile.open(partial, "w") as archive:
            for mode, oid, path in entries:
                process.stdin.write((oid + "\n").encode())
                process.stdin.flush()
                header = process.stdout.readline().split()
                if len(header) != 3 or header[0].decode() != oid or header[1] != b"blob":
                    raise ValueError("Unexpected Git batch response")
                size = int(header[2])
                data = process.stdout.read(size)
                if len(data) != size or process.stdout.read(1) != b"\n":
                    raise ValueError("Truncated Git object")
                info = tarfile.TarInfo(path)
                info.mode = 0o755 if mode == "100755" else 0o644
                if mode == "120000":
                    info.type = tarfile.SYMTYPE
                    info.linkname = data.decode()
                    archive.addfile(info)
                else:
                    info.size = size
                    archive.addfile(info, io.BytesIO(data))
        process.stdin.close()
        if process.wait() != 0:
            raise RuntimeError("Git object export failed")
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait()
    partial.replace(args.archive)
    print("Verified pinned snapshot exported: " + str(args.archive), flush=True)


if __name__ == "__main__":
    main()
