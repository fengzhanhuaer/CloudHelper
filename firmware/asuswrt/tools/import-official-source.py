"""Import the pinned ASUS GPL ZIP into the ignored repository source archive."""
import argparse
import hashlib
import json
import zipfile
from pathlib import Path


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('zip_path', type=Path)
    args = parser.parse_args()
    firmware_root = Path(__file__).resolve().parents[1]
    lock = json.loads((firmware_root / 'baseline.lock.json').read_text(encoding='utf-8'))['source']
    destination = firmware_root / 'upstream/official' / lock['archive_name']
    if destination.exists():
        if sha256(destination) != lock['archive_sha256']:
            raise ValueError('Existing official archive checksum mismatch')
        print('Verified existing official source archive:', destination)
        return
    if sha256(args.zip_path) != lock['download_zip_sha256']:
        raise ValueError('Official ZIP checksum mismatch')
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_suffix(destination.suffix + '.partial')
    try:
        with zipfile.ZipFile(args.zip_path) as archive:
            files = [item for item in archive.infolist() if not item.is_dir()]
            if len(files) != 1 or files[0].filename != lock['archive_name']:
                raise ValueError('Unexpected official ZIP members')
            with archive.open(files[0]) as source, temporary.open('wb') as output:
                for chunk in iter(lambda: source.read(1024 * 1024), b''):
                    output.write(chunk)
        if temporary.stat().st_size != lock['archive_bytes'] or sha256(temporary) != lock['archive_sha256']:
            raise ValueError('Official TGZ checksum/length mismatch')
        temporary.replace(destination)
    finally:
        temporary.unlink(missing_ok=True)
    print('Imported and verified official source archive:', destination)


if __name__ == '__main__':
    main()
