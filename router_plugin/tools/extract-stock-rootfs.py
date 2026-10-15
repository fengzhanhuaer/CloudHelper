#!/usr/bin/env python3
"""Read vendor UBI image, validate headers and reconstruct its SquashFS volume."""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import zlib

p=argparse.ArgumentParser()
p.add_argument('image',type=Path)
p.add_argument('output',type=Path)
args=p.parse_args()
image=args.image.read_bytes()
first=image.find(b'UBI#')
if first<0:
    raise SystemExit('No UBI image')
volumes={}
for offset in range(first,len(image)-131072+1,131072):
    block=image[offset:offset+131072]
    if block[:4]!=b'UBI#':
        continue
    if (zlib.crc32(block[:60])^0xffffffff)!=struct.unpack_from('>I',block,60)[0]:
        raise SystemExit('Bad UBI EC CRC')
    vid_at,data_at=struct.unpack_from('>II',block,16)
    vid=block[vid_at:vid_at+64]
    if vid[:4]!=b'UBI!' or (zlib.crc32(vid[:60])^0xffffffff)!=struct.unpack_from('>I',vid,60)[0]:
        raise SystemExit('Bad UBI VID CRC')
    volume,logical=struct.unpack_from('>II',vid,8)
    seq=struct.unpack_from('>Q',vid,40)[0]
    entries=volumes.setdefault(volume,{})
    if logical not in entries or seq>entries[logical][0]:
        entries[logical]=(seq,block[data_at:])
found=[]
for volume,entries in volumes.items():
    if 0 not in entries or entries[0][1][:4]!=b'hsqs':
        continue
    if sorted(entries)!=list(range(max(entries)+1)):
        raise SystemExit('Missing logical blocks')
    data=b''.join(entries[i][1] for i in range(len(entries)))
    used=struct.unpack_from('<Q',data,40)[0]
    if used>len(data) or used<96:
        raise SystemExit('Invalid SquashFS length')
    found.append(data[:used])
if len(found)!=1:
    raise SystemExit('Expected exactly one SquashFS volume')
args.output.parent.mkdir(parents=True,exist_ok=True)
args.output.write_bytes(found[0])
print(json.dumps({'vendor_image':str(args.image),'vendor_sha256':hashlib.sha256(image).hexdigest(),
                  'squashfs':str(args.output),'bytes':len(found[0]),'sha256':hashlib.sha256(found[0]).hexdigest()},indent=2))
