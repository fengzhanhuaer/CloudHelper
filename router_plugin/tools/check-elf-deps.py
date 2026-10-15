#!/usr/bin/env python3
"""Reject router binaries needing libraries absent from the original rootfs."""
import argparse
import os
import re
import subprocess
from pathlib import Path


ALLOWED = {
    "libcurl.so.4", "libssl.so.1.1", "libcrypto.so.1.1", "libjson-c.so.2",
    "libpthread.so.0", "libc.so.6", "ld-linux.so.3", "libdl.so.2",
}


def inside_root(root, path):
    """Resolve firmware absolute symlinks against its root, never host /lib."""
    for _ in range(40):
        if not path.is_symlink():
            return path if path.is_file() else None
        target = Path(os.readlink(str(path)))
        path = root / str(target).lstrip("/") if target.is_absolute() else path.parent / target
        if ".." in path.relative_to(root).parts:
            return None
    return None


def verify(binary, root, readelf):
    output = subprocess.check_output([readelf, "-h", "-d", str(binary)], universal_newlines=True)
    if not re.search(r"Class:\s+ELF32\b", output) or not re.search(r"Machine:\s+ARM\b", output):
        raise ValueError("expected ARM ELF32 router executable")
    needed = set(re.findall(r"\(NEEDED\).*?\[([^]]+)\]", output))
    if not needed or needed - ALLOWED:
        raise ValueError("unexpected runtime libraries: " + ", ".join(sorted(needed - ALLOWED)))
    if re.search(r"\((?:RUNPATH|RPATH)\)", output):
        raise ValueError("router executable must use system libraries without custom rpath")
    for name in sorted(needed):
        found = None
        for directory in ("lib", "usr/lib"):
            found = inside_root(root, root / directory / name)
            if found:
                break
        if not found:
            raise ValueError("missing original firmware library: " + name)
        print(f"existing firmware dependency: {name}")
    print(f"PASS: {binary.stat().st_size} bytes; no additional runtime library")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("rootfs", type=Path)
    parser.add_argument("--readelf", default="readelf")
    args = parser.parse_args()
    try:
        verify(args.binary, args.rootfs, args.readelf)
    except (ValueError, subprocess.CalledProcessError) as exc:
        parser.exit(1, f"dependency check failed: {exc}\n")
