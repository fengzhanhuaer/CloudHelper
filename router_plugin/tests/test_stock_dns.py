"""Run the unmodified ASUS dnsmasq under QEMU in an isolated container."""
import os
from pathlib import Path
import signal
import socket
import struct
import subprocess
import sys
import threading
import time

if os.environ.get('CHVR_DISPOSABLE_TEST')!='1' or not Path('/.dockerenv').exists():
    raise SystemExit('Disposable container only')
stock=Path('/stock')
stop=threading.Event()
threads=[]
def upstream(port,answer):
    sock=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
    sock.bind(('127.0.0.1',port));sock.settimeout(.2)
    while not stop.is_set():
        try:
            query,peer=sock.recvfrom(4096)
        except socket.timeout:
            continue
        reply=query[:2]+b'\x81\x80\x00\x01\x00\x01\x00\x00\x00\x00'+query[12:]+b'\xc0\x0c\x00\x01\x00\x01\x00\x00\x00\x00\x00\x04'+socket.inet_aton(answer)
        sock.sendto(reply,peer)
    sock.close()
for port,answer in ((15553,'203.0.113.5'),(1053,'198.18.0.20')):
    thread=threading.Thread(target=upstream,args=(port,answer),daemon=True);thread.start();threads.append(thread)
path=Path('/tmp/resolv.dnsmasq');path.write_text('server=127.0.0.1#15553\n')
proc=subprocess.Popen(['qemu-arm','-L',str(stock),'-E','LD_LIBRARY_PATH=/usr/lib:/lib',str(stock/'usr/sbin/dnsmasq'),
    '--keep-in-foreground','--conf-file=/dev/null','--user=root','--group=root','--no-resolv','--no-hosts','--bind-interfaces','--listen-address=127.0.0.1',
    '--port=15353','--cache-size=0','--servers-file='+str(path),'--pid-file=/tmp/stock-dnsmasq.pid'],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
fixture=Path('/tmp/chvr-stock-bin');fixture.mkdir()
(fixture/'killall').write_text('#!/bin/sh\nkill -HUP '+str(proc.pid)+'\n')
(fixture/'killall').chmod(0o700)
env=dict(os.environ,PATH=str(fixture)+':'+os.environ['PATH'])
query=b'\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x04test\x07example\x00\x00\x01\x00\x01'
def lookup(tcp=False):
    sock=socket.socket(socket.AF_INET,socket.SOCK_STREAM if tcp else socket.SOCK_DGRAM);sock.settimeout(1)
    try:
        sock.connect(('127.0.0.1',15353))
        sock.sendall((struct.pack('>H',len(query)) if tcp else b'')+query)
        if tcp:
            head=sock.recv(2);n=struct.unpack('>H',head)[0];body=b''
            while len(body)<n:body+=sock.recv(n-len(body))
        else:body=sock.recv(4096)
        return socket.inet_ntoa(body[-4:])
    finally:sock.close()
try:
    for _ in range(30):
        if proc.poll() is not None:raise AssertionError(proc.stderr.read())
        try:
            if lookup()=='203.0.113.5':break
        except OSError:time.sleep(.05)
    else:raise AssertionError('Stock dnsmasq readiness timeout')
    for enabled,expected in ((1,'198.18.0.20'),(0,'203.0.113.5')):
        subprocess.check_call([sys.argv[1],'--dns',str(enabled)],env=env)
        for _ in range(30):
            if lookup()==expected:break
            time.sleep(.05)
        else:raise AssertionError('SIGHUP did not change upstream')
        assert lookup(True)==expected
    print('PASS: unmodified stock 52332 dnsmasq TCP/UDP with servers-file + SIGHUP takeover/restore')
finally:
    proc.terminate();proc.communicate(timeout=5)
    stop.set()
    for thread in threads:thread.join(timeout=2)
