"""Remote TCP/UDP -> IP frame -> real ASUS-style TUN/LAN SNAT, isolated only."""
import os
from pathlib import Path
import subprocess
import sys

if os.environ.get('CHVR_DISPOSABLE_TEST') != '1' or not Path('/.dockerenv').exists():
    raise SystemExit('Disposable privileged container required; never host networking')

def run(*args):
    try:
        return subprocess.check_output(args, stderr=subprocess.STDOUT, universal_newlines=True)
    except subprocess.CalledProcessError as error:
        print(error.output, file=sys.stderr)
        raise

for ns, interface, local, remote in [
    ('gateway-lan', 'chvrlan', '192.168.52.1', '192.168.52.100'),
    ('gateway-remote', 'chvrwan', '203.0.113.1', '203.0.113.5'),
]:
    run('ip', 'netns', 'add', ns)
    run('ip', 'link', 'add', interface, 'type', 'veth', 'peer', 'name', interface+'p')
    run('ip', 'link', 'set', interface+'p', 'netns', ns)
    run('ip', 'addr', 'add', local+'/24', 'dev', interface)
    run('ip', 'link', 'set', interface, 'up')
    run('ip', '-n', ns, 'addr', 'add', remote+'/24', 'dev', interface+'p')
    run('ip', '-n', ns, 'link', 'set', interface+'p', 'up')
    run('ip', '-n', ns, 'link', 'set', 'lo', 'up')
    run('ip', '-n', ns, 'route', 'add', 'default', 'via', local)
Path('/proc/sys/net/ipv4/ip_forward').write_text('1')
run('iptables', '-P', 'FORWARD', 'DROP')
run('iptables', '-P', 'INPUT', 'DROP')
run('iptables', '-A', 'INPUT', '-m', 'conntrack', '--ctstate', 'ESTABLISHED,RELATED', '-j', 'ACCEPT')
run('iptables', '-A', 'INPUT', '-i', 'chvrlan', '-j', 'ACCEPT')
run('iptables', '-A', 'INPUT', '-i', 'chvrwan', '-p', 'udp', '--dport', '18082', '-j', 'ACCEPT')

server = r'''
import os,socket,threading
ready=threading.Event()
def udp():
 s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind((os.environ['ECHO_IP'],18081));ready.set()
 while True:
  b,a=s.recvfrom(1024);s.sendto(a[0].encode()+b':'+b,a)
threading.Thread(target=udp,daemon=True).start()
s=socket.socket();s.bind((os.environ['ECHO_IP'],18081));s.listen(4);ready.wait();print('READY',flush=True)
while True:
 c,a=s.accept();b=c.recv(1024);c.sendall(a[0].encode()+b':'+b);c.close()
'''
remote = r'''
import os,fcntl,socket,struct,subprocess,select,threading,sys
tun=os.open('/dev/net/tun',os.O_RDWR)
fcntl.ioctl(tun,0x400454ca,struct.pack('16sH',b'remote0',0x1001))
subprocess.check_call(['ip','addr','add','198.18.0.7/32','dev','remote0'])
subprocess.check_call(['ip','link','set','remote0','up'])
subprocess.check_call(['ip','route','add','192.168.52.0/24','dev','remote0','src','198.18.0.7'])
carrier=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);carrier.connect(('203.0.113.1',18082))
source=[7];stop=threading.Event()
def transport():
 while not stop.is_set():
  ready,_,_=select.select([tun,carrier],[],[],.1)
  if tun in ready:carrier.send(bytes(source)+os.read(tun,2048))
  if carrier in ready:os.write(tun,carrier.recv(2048))
thread=threading.Thread(target=transport);thread.start()
try:
 allowed=sys.argv[1]=='gateway'
 for address,expected_source in [('192.168.52.100',b'192.168.52.1'),('192.168.52.1',b'198.18.0.7')]:
  for tcp in (True,False):
   s=socket.socket(socket.AF_INET,socket.SOCK_STREAM if tcp else socket.SOCK_DGRAM);s.settimeout(2 if allowed else .5)
   try:
    s.connect((address,18081));s.sendall(b'authorized');answer=s.recv(1024)
    assert allowed and answer==expected_source+b':authorized', (address,answer)
   except socket.timeout:
    assert not allowed, address
   finally:s.close()
 # Gateway access to the local carrier port must precede its non-LAN guard.
 s=socket.socket();s.settimeout(.5);assert (s.connect_ex(('192.168.52.1',12040))==0)==allowed;s.close()
 source[0]=9
 s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(.7);s.connect(('192.168.52.100',18081));s.send(b'unauthorized')
 try:s.recv(1024);raise AssertionError('unauthorized source reached LAN')
 except socket.timeout:pass
 s.close()
 print('PASS: '+sys.argv[1]+' TCP/UDP LAN SNAT, router INPUT, carrier ordering and unauthorized drop')
finally:stop.set();thread.join(2);carrier.close();os.close(tun)
'''
target = subprocess.Popen(['ip','netns','exec','gateway-lan','env','ECHO_IP=192.168.52.100','python3','-u','-c',server], stdout=subprocess.PIPE, universal_newlines=True)
local = subprocess.Popen(['env','ECHO_IP=192.168.52.1','python3','-u','-c',server], stdout=subprocess.PIPE, universal_newlines=True)
helper = None
try:
    assert target.stdout.readline().strip() == 'READY'
    assert local.stdout.readline().strip() == 'READY'
    for mode in ('gateway','gateway-disabled','gateway-excluded','gateway-revoked'):
        helper = subprocess.Popen([sys.argv[1], mode], stdout=subprocess.PIPE, universal_newlines=True)
        assert helper.stdout.readline().strip() == 'READY'
        rules = run('iptables-save')
        accept = '-A CHVR_IN -s 198.18.0.0/15 -d 192.168.52.1/32 -i chvr0 -j ACCEPT'
        assert (accept in rules) == (mode in ('gateway','gateway-revoked'))
        print(run('ip','netns','exec','gateway-remote','python3','-u','-c',remote,mode).strip())
        helper.terminate();helper.wait(timeout=10);helper=None
        assert 'CHVR_' not in run('iptables-save')
finally:
    if helper is not None:
        helper.terminate();helper.wait(timeout=10)
    target.terminate();target.wait(timeout=5)
    local.terminate();local.wait(timeout=5)
assert 'CHVR_' not in run('iptables-save')
assert 'chvr0' not in run('ip','link','show')
print('PASS: gateway teardown removes owned NAT, routes and TUN')
