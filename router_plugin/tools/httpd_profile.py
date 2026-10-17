"""Verify the pinned BE86U ELF layout without executing vendor instructions."""
import hashlib
from pathlib import Path
import re
import struct


def verify_be86u(path):
    header=(Path(__file__).resolve().parents[1]/'include/httpd_profile.h').read_text()
    blob=Path(path).read_bytes()
    prefix='CHVR_BE86U_'
    digest=hashlib.sha256(blob).hexdigest()
    if digest==re.search(r'^#define CHVR_BE86U_KOOL_HTTPD_SHA256 "([a-f0-9]+)"$',header,re.M)[1]:
        prefix='CHVR_BE86U_KOOL_'
    def value(key):
        return re.search(r'^#define '+prefix+key+r' (.+)$',header,re.M)[1]
    assert len(blob)==int(value('HTTPD_BYTES')), 'Unrecognized HTTPD size'
    assert hashlib.sha256(blob).hexdigest()==value('HTTPD_SHA256').strip('"'), 'Unrecognized HTTPD hash'
    assert blob[:6]==b'\x7fELF\x01\x01' and struct.unpack_from('<HH',blob,16)==(2,40), 'Expected ARM32 EXEC'
    phoff=struct.unpack_from('<I',blob,28)[0]
    phsize,phnum=struct.unpack_from('<HH',blob,42)
    segments=[struct.unpack_from('<8I',blob,phoff+i*phsize) for i in range(phnum)]
    def offset(address,size=4,flags=4):
        for kind,off,addr,_,filesize,memsize,permissions,_ in segments:
            if kind==1 and addr<=address and address+size<=addr+filesize and permissions&flags==flags:
                return off+address-addr
        raise AssertionError('Address outside expected file-backed segment')
    asp=int(value('ASP'),0)
    fields=tuple(int(s,0) for s in value('ASP_FIELDS').strip('{}').split(','))
    assert struct.unpack_from('<6I',blob,offset(asp,24,6))==fields, 'Unexpected ASP callbacks/authentication'
    assert blob[offset(fields[0],8):offset(fields[0],8)+8]==b'**.asp*\0'
    for address in fields[3:]:offset(address,4,5)
    # Both SSL globals were traced from the HTTPD accept/TLS branch, via GOT.
    for name in ('SSL_ENABLED','SSL_FD'):
        got=int(value(name+'_GOT'),0)
        address=int(value(name),0)
        assert struct.unpack_from('<I',blob,offset(got))[0]==address
        assert any(kind==1 and flags&6==6 and addr<=address and address+4<=addr+size
                   for kind,_,addr,_,_,size,flags,_ in segments)
    return True
