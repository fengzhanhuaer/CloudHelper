"""Verify generated Broadcom image tokens, capacity, rootfs and ARM ELF inputs."""
import argparse
import hashlib
import json
import re
import struct
import subprocess
import zlib
from pathlib import Path


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('task_root', type=Path)
    args = parser.parse_args()
    firmware_root = Path(__file__).resolve().parents[1]
    lock = json.loads((firmware_root / 'baseline.lock.json').read_text(encoding='utf-8'))
    target = args.task_root / lock['source']['linux_source_relative_path'] / lock['target']['directory'] / 'targets' / lock['target']['profile']
    source_root = args.task_root / lock['source']['linux_source_relative_path']
    for path in (target / 'fs').rglob('*'):
        if re.fullmatch(r'core(?:\.\d+)?', path.name) and path.is_file():
            with path.open('rb') as stream:
                header = stream.read(20)
            if len(header) == 20 and header[:4] == b'\x7fELF' and header[5] in (1, 2):
                if struct.unpack(('<' if header[5] == 1 else '>') + 'H', header[16:18])[0] == 4:
                    raise ValueError('Host core dump must not enter firmware: ' + str(path.relative_to(target / 'fs')))
    router_config = (source_root / 'release/src/router/.config').read_text()
    if 'RTCONFIG_BRCM_NAND_JFFS2=y\n' not in router_config or 'RTCONFIG_UBIFS=y\n' in router_config:
        raise ValueError('Expected native stock JFFS2 configuration, without UBIFS data migration')
    version_header = (source_root / 'release/src/router/shared/version.h').read_text()
    if '#define RT_EXTENDNO "53297-gee411da"' not in version_header:
        raise ValueError('Generated firmware version does not match the pinned official source')
    images = []
    image_payloads = {}
    for path in sorted((args.task_root / 'artifacts').glob('*.w')):
        data = path.read_bytes()
        # WFI_TAG: CRC, version/magic, chip ID, NAND type, flags (bcmTag.h).
        image_crc, token_magic, chip, flash_type, flags = struct.unpack('<5I', data[-20:])
        actual_crc = zlib.crc32(data[:-20]) ^ 0xffffffff
        if image_crc != actual_crc or token_magic != 0x5732 or chip != 0x47622 or flash_type != 3:
            raise ValueError('Broadcom image token mismatch: ' + path.name)
        if len(data) > lock['target']['image_partition_bytes']:
            raise ValueError('Image exceeds current image partition: ' + path.name)
        ubi_offset = data.find(b'UBI#')
        if ubi_offset < 0:
            raise ValueError('UBI erase-counter header missing: ' + path.name)
        header = data[ubi_offset:ubi_offset + 64]
        header_crc = struct.unpack('>I', header[-4:])[0]
        if header_crc != zlib.crc32(header[:-4]) ^ 0xffffffff:
            raise ValueError('UBI header checksum mismatch: ' + path.name)
        images.append(dict(name=path.name, bytes=len(data), sha256=hashlib.sha256(data).hexdigest(),
                           image_crc32=f'{image_crc:08x}', token_magic=token_magic, chip=chip,
                           flash_type=flash_type, flags=flags, ubi_offset=ubi_offset, fits_partition=True))
        image_payloads[path.name] = data
    if len(images) != 2:
        raise ValueError('Expected both generated puresqubi images')
    with_cferom = next(data for name, data in image_payloads.items() if name.endswith('_cferom_puresqubi.w'))
    system_only = next(data for name, data in image_payloads.items() if not name.endswith('_cferom_puresqubi.w'))
    if len(with_cferom) - len(system_only) != 1048576 or with_cferom[1048576:-20] != system_only[:-20]:
        raise ValueError('Expected identical system payloads with a 1 MiB CFE ROM prefix difference')
    elf_files = []
    for relative in ('bin/busybox', 'usr/sbin/dnsmasq', 'usr/sbin/httpd', 'sbin/rc'):
        path = target / 'fs' / relative
        data = path.read_bytes()
        if data[:6] != b'\x7fELF\x01\x01' or struct.unpack_from('<H', data, 18)[0] != 40:
            raise ValueError('Expected little-endian ELF32 ARM: ' + relative)
        elf_files.append(dict(path=relative, bytes=len(data), architecture='ELF32 ARM', sha256=hashlib.sha256(data).hexdigest()))
    squashfs = (target / 'squashfs.img').read_bytes()
    if squashfs[:4] != b'hsqs' or struct.unpack_from('<H', squashfs, 28)[0] != 4:
        raise ValueError('Expected SquashFS v4 root filesystem')
    modules = [str(p.relative_to(target / 'fs')) for p in (target / 'fs/lib/modules').rglob('tun.ko')]
    if not modules:
        raise ValueError('Built root filesystem is missing tun.ko')
    patches = [dict(path=str(path.relative_to(firmware_root)), sha256=hashlib.sha256(path.read_bytes()).hexdigest())
               for path in sorted((firmware_root / 'patches').glob('*.patch'))
               if subprocess.run(['git', 'apply', '--reverse', '--check', str(path)],
                                 cwd=str(source_root), stdout=subprocess.DEVNULL,
                                 stderr=subprocess.DEVNULL).returncode == 0]
    if (firmware_root / 'patches/0001-rtax55-preserve-stock-jffs2.patch').exists():
        if b'Preserved original RT-AX55/V2 JFFS2 partition' not in (target / 'fs/sbin/rc').read_bytes():
            raise ValueError('Built rc is missing the stock JFFS2 compatibility patch')
    report = dict(source_commit=lock['source']['commit'], source_version=lock['source']['version'], source_archive_sha256=lock['source']['archive_sha256'], target=lock['target']['make_target'], patches=patches,
                  images=images, arm_elf_files=elf_files, tun_modules=modules,
                  rootfs_bytes=len(squashfs), system_payloads_identical=True, cferom_prefix_bytes=1048576,
                  generated_runtime_version='53297-gee411da', compiled_native_jffs2=True,
                  on_device_boot_verified=False)
    output = args.task_root / 'artifacts/baseline-validation.json'
    output.write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
