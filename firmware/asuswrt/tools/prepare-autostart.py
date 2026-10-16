#!/usr/bin/env python3
"""Apply the minimal variant to the pinned official tree; retain build caches."""
import difflib
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tarfile


def replace_once(text, old, new):
    if text.count(old) != 1:
        raise ValueError('Official patch anchor is not unique: ' + old[:100])
    return text.replace(old, new, 1)


def main():
    firmware = Path(__file__).resolve().parents[1]
    lock = json.loads((firmware / 'baseline.lock.json').read_text())
    task = Path(lock['source']['linux_task_root'])
    source = task / lock['source']['linux_source_relative_path']
    archive = firmware / 'upstream/official' / lock['source']['archive_name']
    digest = hashlib.sha256()
    with archive.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    if digest.hexdigest() != lock['source']['archive_sha256']:
        raise ValueError('Official archive checksum mismatch')
    patch = firmware / 'patches/0002-jffs-autostart.patch'
    legacy = firmware / 'patches/0001-virtual-router-integration.patch'
    # Revert only our exact old variant. Unknown edits are rejected, never reset.
    reverse = ['git', 'apply', '--reverse']
    if subprocess.run(reverse + ['--check', str(legacy)], cwd=source, capture_output=True).returncode == 0:
        subprocess.run(reverse + [str(legacy)], cwd=source, check=True)
    originals = {}
    paths = ['release/src/router/rc/services.c', 'release/src/router/Makefile',
             'release/src/router/httpd/web.c', 'release/src/router/www/require/modules/menuTree.js']
    with tarfile.open(str(archive), 'r:gz') as tar:
        for relative in paths:
            originals[relative] = tar.extractfile('asuswrt/' + relative).read().decode('utf-8-sig')
    changes = {}
    relative = paths[0]
    text = originals[relative]
    begin = text.index('start_services(void)\n{')
    end = text.index('\n}\n', begin)
    tail = text[begin:end]
    tail = replace_once(tail, '\treturn 0;',
        '\t/* CloudHelper: never block stock boot on a JFFS plugin. */\n'
        '\txstart("/bin/sh", "/rom/etc/cloudhelper-jffs-start");\n\treturn 0;')
    changes[relative] = text[:begin] + tail + text[end:]
    relative = paths[1]
    text = originals[relative]
    anchor = 'rc-install:\n\t$(MAKE) -C rc INSTALLDIR=$(INSTALLDIR)/rc install'
    text = replace_once(text, anchor, anchor +
        '\n\tinstall -D -m 755 rc/cloudhelper-jffs-start $(INSTALLDIR)/rc/rom/etc/cloudhelper-jffs-start')
    # All package executables have been installed/stripped before reinstall.
    anchor = '\trm -rf $(INSTALLDIR)/rom/rom/etc'
    text = replace_once(text, anchor, anchor +
        '\n\tsha256sum $(INSTALLDIR)/usr/sbin/httpd | cut -d " " -f 1 > $(INSTALLDIR)/rom/etc/cloudhelper-httpd.sha256')
    changes[relative] = text
    relative = 'release/src/router/rc/cloudhelper-jffs-start'
    originals[relative] = ''
    changes[relative] = (firmware / 'overlay/rom/etc/cloudhelper-jffs-start').read_text()
    content = ''.join(''.join(difflib.unified_diff(originals[p].splitlines(True), new.splitlines(True),
        fromfile='a/' + p if originals[p] else '/dev/null', tofile='b/' + p)) for p, new in changes.items())
    if patch.exists() and patch.read_text() != content:
        raise ValueError('Regenerated patch differs; review the tracked patch before rebuilding')
    patch.write_text(content)
    already = subprocess.run(reverse + ['--check', str(patch)], cwd=source, capture_output=True).returncode == 0
    for p in paths:
        expected = changes.get(p, originals[p]) if already else originals[p]
        if (source / p).read_text(encoding='utf-8-sig') != expected:
            raise ValueError('Unexpected vendor-tree modification: ' + p)
    if not already:
        subprocess.run(['git', 'apply', '--check', str(patch)], cwd=source, check=True)
        subprocess.run(['git', 'apply', str(patch)], cwd=source, check=True)
    # Remove only known generated legacy payloads from staging and package roots.
    # Their signed-off historical images remain in artifacts/official-53297.
    names = ['usr/sbin/cloudhelper-router-plugin', 'usr/sbin/cloudhelper-router-service',
             'www/VirtualRouter.asp', 'VirtualRouter.asp', 'rom/etc/cloudhelper-router-build.json']
    target = source / lock['target']['directory'] / 'targets' / lock['target']['profile']
    for root in [target / 'fs', target / 'fs/cloudhelper-router']:
        for name in names:
            path = root / name
            if path.is_file():
                path.unlink()
    legacy_stage = target / 'fs/cloudhelper-router'
    if legacy_stage.is_dir():
        if any(p.is_file() for p in legacy_stage.rglob('*')):
            raise ValueError('Unknown legacy staging files retained: ' + str(legacy_stage))
        shutil.rmtree(str(legacy_stage))
    print('Prepared minimal JFFS startup variant; original HTTPD and menu sources retained')


if __name__ == '__main__':
    main()
