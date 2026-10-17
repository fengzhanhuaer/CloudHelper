#!/usr/bin/env python3
"""Recreate the plugin SDK and ABI fixtures from pinned public vendor inputs."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import tarfile
import time
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def download(record, cache):
    path = cache / record['url'].rsplit('/', 1)[1]
    for attempt in range(4):
        if path.is_file() and path.stat().st_size == record['bytes']:
            if sha256(path) == record['sha256']:
                return path
            path.unlink()
        partial = path.with_suffix(path.suffix + '.part')
        offset = partial.stat().st_size if partial.exists() else 0
        request = urllib.request.Request(record['url'], headers={'Range': f'bytes={offset}-', 'User-Agent': 'CloudHelper-plugin-build'})
        try:
            with urllib.request.urlopen(request, timeout=45) as response:
                resume = offset and response.status == 206 and response.headers.get('Content-Range', '').startswith(f'bytes {offset}-')
                with partial.open('ab' if resume else 'wb') as output:
                    while True:
                        block = response.read(1024 * 1024)
                        if not block:
                            break
                        output.write(block)
                        if output.tell() > record['bytes']:
                            raise ValueError('download exceeds pinned input size')
            partial.replace(path)
            if path.stat().st_size == record['bytes'] and sha256(path) == record['sha256']:
                return path
        except (OSError, ValueError):
            if attempt == 3:
                raise
            time.sleep(2 ** attempt)
    raise ValueError('input size or SHA256 mismatch: ' + record['url'])


def firmware_rootfs(path, destination):
    if zipfile.is_zipfile(path):
        with zipfile.ZipFile(path) as archive:
            names = [n for n in archive.namelist() if n.endswith(('.pkgtb', '.w', '.trx'))]
            names.sort(key=lambda n: ('cferom' in n, len(n)))
            image = archive.read(names[0])
    else:
        image = path.read_bytes()
    start = image.find(b'UBI#')
    if start >= 0:
        volumes = {}
        for offset in range(start, len(image) - 64, 128 * 1024):
            if image[offset:offset + 4] != b'UBI#':
                continue
            vid, data = struct.unpack_from('>II', image, offset + 16)
            if not 64 <= vid < data < 128 * 1024 or image[offset + vid:offset + vid + 4] != b'UBI!':
                raise ValueError('invalid UBI headers')
            volume, number = struct.unpack_from('>II', image, offset + vid + 8)
            volumes.setdefault(volume, {})[number] = image[offset + data:offset + 128 * 1024]
        matches = [blocks for blocks in volumes.values() if blocks.get(0, b'').startswith(b'hsqs')]
        if len(matches) != 1:
            raise ValueError('expected one SquashFS UBI volume')
        blocks = matches[0]
        if sorted(blocks) != list(range(len(blocks))):
            raise ValueError('incomplete SquashFS volume')
        image = b''.join(blocks[i] for i in range(len(blocks)))
    else:
        start = image.find(b'hsqs')
        if start < 0:
            raise ValueError('no SquashFS payload')
        image = image[start:]
    if image[:4] != b'hsqs':
        raise ValueError('invalid SquashFS image')
    size = struct.unpack_from('<Q', image, 40)[0]
    if size > len(image):
        raise ValueError('truncated SquashFS')
    packed = destination.with_suffix('.squashfs')
    packed.parent.mkdir(parents=True, exist_ok=True)
    packed.write_bytes(image[:size])
    subprocess.run(['unsquashfs', '-no-progress', '-processors', '2', '-d', str(destination), str(packed)], check=True, stdout=subprocess.DEVNULL)
    packed.unlink()


def sdk(archive, destination):
    # Only SDK and public headers/configure tools are needed. Never compile the OS.
    sdk_name = 'crosstools-arm-gcc-5.5-linux-4.1-glibc-2.26-binutils-2.28.1'
    chain = '/toolchains/' + sdk_name + '/'
    paths = {'/release/src/router/openssl-1.1/': 'openssl',
             '/release/src/router/json-c/': 'json-c',
             '/release/src/router/curl/include/': 'include'}
    with zipfile.ZipFile(archive) as outer, outer.open(outer.namelist()[0]) as stream, tarfile.open(fileobj=stream, mode='r|gz') as source:
        for entry in source:
            name = entry.name
            relative = None
            if chain in name:
                relative = 'toolchain/' + name.split(chain, 1)[1]
            else:
                for marker, prefix in paths.items():
                    if marker in name:
                        relative = prefix + '/' + name.split(marker, 1)[1]
                        break
            if relative is None:
                continue
            if '..' in Path(relative).parts or relative.startswith('/'):
                raise ValueError('unexpected SDK input path')
            target = destination / relative
            if entry.isdir():
                target.mkdir(parents=True, exist_ok=True)
            elif entry.isfile():
                target.parent.mkdir(parents=True, exist_ok=True)
                with source.extractfile(entry) as inp, target.open('wb') as out:
                    shutil.copyfileobj(inp, out)
                target.chmod(entry.mode & 0o777)
            elif entry.issym():
                if entry.linkname.startswith('/') or '..' in Path(entry.linkname).parts:
                    # Compiler internal links can have bounded parent references.
                    resolved = (target.parent / entry.linkname).resolve()
                    if destination.resolve() not in resolved.parents:
                        raise ValueError('SDK symlink escapes destination')
                target.parent.mkdir(parents=True, exist_ok=True)
                target.symlink_to(entry.linkname)
    compiler = destination / 'toolchain/bin/arm-buildroot-linux-gnueabi-gcc'
    environment = dict(os.environ, LD_LIBRARY_PATH=str(destination / 'toolchain/lib'), CC=str(compiler))
    subprocess.run(['./Configure', 'linux-armv4', 'no-asm', 'no-tests'], cwd=destination / 'openssl', env=environment, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(['make', '-s', 'build_generated'], cwd=destination / 'openssl', env=environment, check=True, stdout=subprocess.DEVNULL)
    subprocess.run(['./configure', '--host=arm-buildroot-linux-gnueabi', '--disable-shared'], cwd=destination / 'json-c', env=environment, check=True, stdout=subprocess.DEVNULL)
    (destination / 'include/json-c').symlink_to('../json-c')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('--cache', type=Path, required=True)
    args = parser.parse_args()
    destination = args.directory.resolve()
    destination.mkdir(parents=True, exist_ok=True)
    args.cache.mkdir(parents=True, exist_ok=True)
    lock = json.loads((ROOT / 'ci-inputs.lock.json').read_text())
    expected = hashlib.sha256((ROOT / 'ci-inputs.lock.json').read_bytes() + Path(__file__).read_bytes()).hexdigest()
    stamp = destination / '.prepared'
    if stamp.exists() and stamp.read_text() == expected:
        print('Pinned plugin SDK and four firmware fixtures restored from cache')
        return
    if any(destination.iterdir()):
        raise SystemExit('Use an empty directory when changing the CI input lock')
    sdk(download(lock['sdk'], args.cache), destination)
    for profile, record in lock['firmware'].items():
        firmware_rootfs(download(record, args.cache), destination / 'rootfs' / profile)
        httpd = destination / 'rootfs' / profile / 'usr/sbin/httpd'
        if sha256(httpd) != record['httpd_sha256']:
            raise ValueError('firmware does not match the supported HTTPD: ' + profile)
    stamp.write_text(expected)
    print('Public SDK and four exact firmware ABI fixtures prepared; no system build')


if __name__ == '__main__':
    main()
