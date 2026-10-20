"""Run only in a disposable Linux network namespace with CAP_NET_ADMIN.

Example: docker run --rm --cap-add NET_ADMIN -e CHVR_DISPOSABLE_TEST=1 -v "$PWD:/module" \
  cloudhelper-router-ci python3 tests/test_httpd_gateway_network.py
"""
import os
from pathlib import Path
import socket
import struct
import subprocess
import sys

if os.environ.get('CHVR_DISPOSABLE_TEST')!='1':
    raise SystemExit('Set CHVR_DISPOSABLE_TEST=1 only inside a disposable network namespace')

root=Path(__file__).resolve().parents[1]
created=[]
net=socket.socket(socket.AF_NETLINK,socket.SOCK_RAW,0)
net.bind((0,0))
net.settimeout(3)
sequence=0

def attr(kind,data):
    length=4+len(data)
    return struct.pack('HH',length,kind)+data+b'\0'*((-length)%4)

def request(kind,flags,payload):
    global sequence
    sequence+=1
    net.sendto(struct.pack('IHHII',16+len(payload),kind,flags|1|4,sequence,0)+payload,(0,0))
    reply=net.recv(8192)
    length,kind,_,seq,_=struct.unpack_from('IHHII',reply)
    assert seq==sequence and kind==2 and length>=20,'unexpected netlink acknowledgement'
    error=struct.unpack_from('i',reply,16)[0]
    if error:raise OSError(-error,os.strerror(-error))

def interface(name,ip,prefix):
    try:socket.if_nametoindex(name)
    except OSError:pass
    else:raise RuntimeError('requires a disposable namespace without '+name)
    info=struct.pack('BBHiII',0,0,0,0,0,0)
    request(16,0x400|0x200,info+attr(3,name.encode()+b'\0')+attr(18,attr(1,b'dummy\0')))
    index=socket.if_nametoindex(name)
    created.append(index)
    address=socket.inet_aton(ip)
    request(20,0x400|0x200,struct.pack('BBBBI',socket.AF_INET,prefix,0,0,index)+attr(1,address)+attr(2,address))
    request(16,0,struct.pack('BBHiII',0,0,0,index,1,1))

try:
    interface('chvr0','198.18.0.8',15)
    interface('other0','198.20.0.8',24)
    subprocess.run([sys.executable,str(root/'tests/test_httpd_bridge.py')],
        env={**os.environ,'CHVR_TEST_GATEWAY_NETWORK':'1'},check=True)
finally:
    for index in reversed(created):
        request(17,0,struct.pack('BBHiII',0,0,0,index,0,0))
    net.close()
