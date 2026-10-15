#!/usr/bin/env python3
"""Verify a vendor dnsmasq on a separate loopback port, without touching system DNS."""
import argparse
import json
from pathlib import Path
import re
import secrets
import shlex
import socket
import struct
import subprocess
import time

p=argparse.ArgumentParser()
p.add_argument('--ssh',default='localrouter')
p.add_argument('--dnsmasq',type=Path,help='Optional vendor binary; default is the running stock binary')
args=p.parse_args()
ssh=['ssh','-o','BatchMode=yes','-o','ConnectTimeout=8',args.ssh]
def remote(command, data=None):
    return subprocess.check_output(ssh+[command],input=data,timeout=15)
remote("if netstat -ln | grep -q ':15353 '; then exit 1; fi")
user=remote("awk -F: '$3==0 {print $1;exit}' /etc/passwd").decode().strip()
group=remote("awk -F: '$3==0 {print $1;exit}' /etc/group").decode().strip()
directory='/tmp/chvr-stockdns.'+secrets.token_hex(8)
remote('umask 077 && mkdir '+shlex.quote(directory))
if not re.fullmatch(r'/tmp/chvr-stockdns\.[A-Za-z0-9]+',directory):
    raise SystemExit('Unexpected temporary directory')
q=shlex.quote
forward=None
pid=None
try:
    binary=args.dnsmasq.read_bytes() if args.dnsmasq else remote('cat /usr/sbin/dnsmasq')
    remote('cat > '+q(directory+'/dnsmasq-stock')+' && chmod 700 '+q(directory+'/dnsmasq-stock'),binary)
    upstreams=remote('cat /tmp/resolv.dnsmasq')
    # Use the router's effective vendor upstream, which may be a private gateway.
    # Public resolvers can be blocked even while normal router DNS works.
    if not re.search(rb'^server=(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?:#[0-9]+)?$',upstreams,re.M):
        raise RuntimeError('No usable effective vendor upstream')
    remote('cat > '+q(directory+'/servers'),upstreams)
    command=q(directory+'/dnsmasq-stock')+' --keep-in-foreground --conf-file=/dev/null --user='+q(user)+' --group='+q(group)+' --no-resolv --no-hosts --bind-interfaces --listen-address=127.0.0.1 --port=15353 --cache-size=0 --log-facility=- --servers-file='+q(directory+'/servers')+' --pid-file='+q(directory+'/pid')+' </dev/null >/dev/console 2>&1 & echo $!'
    pid=remote(command).decode().strip()
    if not pid.isdigit():raise RuntimeError('Invalid child PID')
    with socket.socket() as local:
        local.bind(('127.0.0.1',0));port=local.getsockname()[1]
    forward=subprocess.Popen(ssh[:-1]+['-o','ExitOnForwardFailure=yes','-N','-L','127.0.0.1:'+str(port)+':127.0.0.1:15353',args.ssh],stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
    query=b'\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01'
    def lookup():
        with socket.create_connection(('127.0.0.1',port),timeout=4) as sock:
            sock.settimeout(5);sock.sendall(struct.pack('>H',len(query))+query)
            def read(n):
                out=b''
                while len(out)<n:
                    chunk=sock.recv(n-len(out))
                    if not chunk:raise RuntimeError('DNS stream closed')
                    out+=chunk
                return out
            n=struct.unpack('>H',read(2))[0];reply=read(n)
            return reply[3]&15,struct.unpack_from('>H',reply,6)[0]
    for _ in range(20):
        try:
            initial=lookup()
            if initial[0]==0 and initial[1]>0:break
        except (OSError,RuntimeError):pass
        time.sleep(.2)
    else:raise RuntimeError('Isolated vendor DNS did not resolve via its upstream')
    # A local-only domain yields NXDOMAIN and makes a reload observable immediately.
    remote('cat > '+q(directory+'/servers')+' && kill -HUP '+pid,b'server=/example.com/\n')
    time.sleep(.2)
    switched=lookup()
    if switched[0]!=3:raise RuntimeError('Vendor servers-file SIGHUP did not select local-only domain')
    remote('cat > '+q(directory+'/servers')+' && kill -HUP '+pid,upstreams)
    time.sleep(.2)
    restored=lookup()
    if restored[0]!=0 or not restored[1]:raise RuntimeError('Vendor servers-file restore failed')
    print(json.dumps({'result':'PASS','scope':'vendor binary on isolated loopback:15353; system DNS and JFFS untouched',
                      'initial':initial,'changed':switched,'restored':restored}))
finally:
    if forward:
        forward.terminate()
        try:forward.communicate(timeout=5)
        except subprocess.TimeoutExpired:forward.kill();forward.communicate()
    if pid and pid.isdigit():
        remote('if test "$(readlink /proc/'+pid+'/exe)" = '+q(directory+'/dnsmasq-stock')+'; then kill -TERM '+pid+'; fi')
    remote('rm -f '+q(directory+'/servers')+' '+q(directory+'/pid')+' '+q(directory+'/dnsmasq-stock')+' && rmdir '+q(directory))
