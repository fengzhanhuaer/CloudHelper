#!/usr/bin/env python3
"""Check the exact packed rootfs and export only the no-CFE startup image."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('task_root', type=Path)
    args = parser.parse_args()
    firmware = Path(__file__).resolve().parents[1]
    lock = json.loads((firmware / 'baseline.lock.json').read_text())
    source = args.task_root / lock['source']['linux_source_relative_path']
    target = source / lock['target']['directory'] / 'targets' / lock['target']['profile']
    baseline = json.loads((args.task_root / 'artifacts/baseline-validation.json').read_text())
    image = next(i for i in baseline['images'] if not i['name'].endswith('_cferom_puresqubi.w'))
    if image['ubi_offset'] != 0 or image['flags'] != 2:
        raise ValueError('Require a system-only image without CFE ROM prefix')
    script = firmware / 'overlay/rom/etc/cloudhelper-jffs-start'
    # Extract the compressed filesystem: the staging directory alone is not proof.
    with tempfile.TemporaryDirectory(prefix='chvr-packed-') as directory:
        root = Path(directory) / 'fs'
        squashfs = Path(directory) / 'packed.squashfs'
        subprocess.run(['python3', str(firmware.parents[1] / 'router_plugin/tools/extract-stock-rootfs.py'),
                        str(args.task_root / 'artifacts' / image['name']), str(squashfs)], check=True)
        subprocess.run(['unsquashfs', '-no-progress', '-d', str(root), str(squashfs)],
                       check=True, stdout=subprocess.DEVNULL)
        startup = root / 'rom/etc/cloudhelper-jffs-start'
        if startup.read_bytes() != script.read_bytes() or not startup.stat().st_mode & 0o111:
            raise ValueError('Packed startup script missing or differs from reviewed source')
        rc = root / 'sbin/rc'
        if b'/rom/etc/cloudhelper-jffs-start\0' not in rc.read_bytes():
            raise ValueError('Packed rc lacks startup hook')
        httpd_hash = sha(root / 'usr/sbin/httpd')
        if (root / 'rom/etc/cloudhelper-httpd.sha256').read_text().strip() != httpd_hash:
            raise ValueError('Packed HTTPD differs from the read-only ABI declaration')
        for relative in ['usr/sbin/cloudhelper-router-plugin', 'usr/sbin/cloudhelper-router-service',
                         'www/VirtualRouter.asp', 'VirtualRouter.asp', 'rom/etc/cloudhelper-router-build.json']:
            if (root / relative).exists():
                raise ValueError('Unexpected old built-in business payload: ' + relative)
        if b'cloudhelper_router.cgi' in (root / 'usr/sbin/httpd').read_bytes():
            raise ValueError('Legacy HTTPD business handler remains in the minimal firmware')
        # ABI exports required by the independent plugin bridge.
        symbols = subprocess.check_output(['readelf', '--dyn-syms', '--wide', str(root / 'usr/sbin/httpd')], text=True)
        for name in ['mime_handlers', 'do_ssl', 'ssl_stream_fd']:
            if not any(line.split()[-1:] == [name] and ' OBJECT ' in line for line in symbols.splitlines()):
                raise ValueError('Required HTTPD ABI export missing: ' + name)
        busybox_version = subprocess.check_output(['qemu-arm', '-L', str(root),
            str(root / 'bin/busybox'), 'echo', 'packed ARM shell runs'], text=True).strip()
        if busybox_version != 'packed ARM shell runs':
            raise ValueError('Packed ARM runtime failed')
        subprocess.run(['qemu-arm', '-L', str(root), str(root / 'bin/busybox'),
                        'sh', '-n', str(startup)], check=True)
        packed = {name: {'bytes': (root / name).stat().st_size, 'sha256': sha(root / name)}
            for name in ['sbin/rc', 'usr/sbin/httpd', 'rom/etc/cloudhelper-jffs-start',
                         'rom/etc/cloudhelper-httpd.sha256', 'www/require/modules/menuTree.js']}
    archive = firmware / 'upstream/official' / lock['source']['archive_name']
    with tarfile.open(str(archive), 'r:gz') as tar:
        for relative in ['release/src/router/httpd/web.c', 'release/src/router/www/require/modules/menuTree.js']:
            original = tar.extractfile('asuswrt/' + relative).read().decode('utf-8-sig')
            if (source / relative).read_text(encoding='utf-8-sig') != original:
                raise ValueError('Minimal firmware must retain original vendor source: ' + relative)
    output = firmware / 'artifacts/autostart'
    output.mkdir(parents=True, exist_ok=True)
    name = 'CloudHelper_RT-AX56U_V2_53297_jffs-autostart_' + image['sha256'][:12] + '_puresqubi.w'
    path = output / name
    source_image = args.task_root / 'artifacts' / image['name']
    if sha(source_image) != image['sha256']:
        raise ValueError('Image changed since baseline validation')
    shutil.copyfile(str(source_image), str(path))
    report = dict(variant='minimal-jffs-autostart', source=lock['source'],
        target=lock['target']['make_target'], device='RT-AX56U_V2',
        image=dict(image, name=name), packed_files=packed,
        patch_sha256=sha(firmware / 'patches/0002-jffs-autostart.patch'),
        packed_rootfs_verified=True, arm_shell_verified=True,
        original_web_sources_verified=True, business_payload_in_firmware=False,
        requires_updated_jffs_service=True, on_device_boot_verified=False,
        disable_autostart='/jffs/cloudhelper-router/autostart.disabled',
        scope='Offline build verification only; no router flash or reboot performed')
    (output / (name + '.json')).write_text(json.dumps(report, indent=2) + '\n')
    (output / (name + '.sha256')).write_text(image['sha256'] + '  ' + name + '\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
