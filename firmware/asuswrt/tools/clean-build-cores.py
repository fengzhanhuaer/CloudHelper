"""Remove only ELF core dumps left by host-side vendor web build tools."""
import argparse
import re
import stat
import struct
from pathlib import Path


def clean(directory):
    removed = 0
    for path in directory.iterdir():
        if not re.fullmatch(r'core(?:\.\d+)?', path.name):
            continue
        if not stat.S_ISREG(path.lstat().st_mode):
            continue
        with path.open('rb') as stream:
            header = stream.read(20)
        if len(header) != 20 or header[:4] != b'\x7fELF' or header[5] not in (1, 2):
            continue
        if struct.unpack(('<' if header[5] == 1 else '>') + 'H', header[16:18])[0] != 4:
            continue
        path.unlink()
        removed += 1
    print('Removed {} host ELF core dumps from {}'.format(removed, directory))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source_root', type=Path)
    args = parser.parse_args()
    clean(args.source_root / 'release/src/router/www')
