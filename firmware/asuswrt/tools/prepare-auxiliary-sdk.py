"""Materialize only the pinned legacy HND helper SDK, without a Git checkout."""
import hashlib
import json
import tarfile
from pathlib import Path


def main():
    root = Path(__file__).resolve().parents[1]
    lock = json.loads((root / 'baseline.lock.json').read_text())
    spec = lock['auxiliary_toolchains']
    # This helper runs in native WSL; the downloaded input lives on Windows.
    drive, relative = spec['archive_path'].split(':/', 1)
    archive = Path('/mnt') / drive.lower() / relative
    expected = spec['archive_sha256']
    digest = hashlib.sha256()
    with archive.open('rb') as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(chunk)
    if digest.hexdigest() != expected:
        raise ValueError('Pinned auxiliary SDK checksum mismatch')
    destination = Path(spec['linux_directory'])
    marker = destination / '.archive.sha256'
    if marker.exists():
        if marker.read_text().strip() != expected:
            raise ValueError('Auxiliary SDK identity mismatch')
        return
    destination.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive) as source:
        members = [item for item in source.getmembers()
                   if item.name == 'fwtag.ini' or item.name == 'hndtools-armeabi-2013.11'
                   or item.name.startswith('hndtools-armeabi-2013.11/')]
        if not any(item.name == 'fwtag.ini' for item in members):
            raise ValueError('Pinned SDK is missing fwtag.ini')
        for item in members:
            if Path(item.name).is_absolute() or '..' in Path(item.name).parts:
                raise ValueError('Invalid auxiliary SDK member path')
        # The trusted, hash-pinned SDK contains toolchain symlinks; preserve them.
        source.extractall(destination, members=members, filter='fully_trusted')
    if not (destination / 'hndtools-armeabi-2013.11').is_dir():
        raise ValueError('Pinned SDK is missing the HND compiler directory')
    marker.write_text(expected + '\n')
    print('Prepared pinned HND helper SDK without Git metadata')


if __name__ == '__main__':
    main()
