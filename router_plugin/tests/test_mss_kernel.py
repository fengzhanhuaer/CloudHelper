"""Bounded real-kernel MSS regression; disposable network container only."""
import os
from pathlib import Path
import select
import signal
import subprocess
import sys

if os.environ.get('CHVR_DISPOSABLE_TEST') != '1' or not Path('/.dockerenv').exists():
    raise SystemExit('Disposable privileged container required; never host networking')


def run(*args):
    return subprocess.check_output(args, stderr=subprocess.STDOUT, universal_newlines=True)


def line(proc):
    assert select.select([proc.stdout], [], [], 10)[0], ('timeout', proc.poll())
    return proc.stdout.readline().strip()


for ns, interface, local, peer in [
    ('mss-lan', 'chvrlan', '192.168.52.1', '192.168.52.100'),
    ('mss-remote', 'chvrwan', '203.0.113.1', '203.0.113.5'),
]:
    run('ip', 'netns', 'add', ns)
    run('ip', 'link', 'add', interface, 'type', 'veth', 'peer', 'name', interface+'p')
    run('ip', 'link', 'set', interface+'p', 'netns', ns)
    run('ip', 'addr', 'add', local+'/24', 'dev', interface)
    run('ip', 'link', 'set', interface, 'up')
    run('ip', '-n', ns, 'addr', 'add', peer+'/24', 'dev', interface+'p')
    run('ip', '-n', ns, 'link', 'set', interface+'p', 'up')
    run('ip', '-n', ns, 'link', 'set', 'lo', 'up')
    run('ip', '-n', ns, 'route', 'add', 'default', 'via', local)
    run('ip', 'netns', 'exec', ns, 'sysctl', '-qw', 'net.ipv4.tcp_timestamps=0')
Path('/proc/sys/net/ipv4/ip_forward').write_text('1')
# Keep a separate ordinary forwarded path for the isolation check.
run('iptables', '-A', 'FORWARD', '-p', 'tcp', '--dport', '18081', '-j', 'ACCEPT')
run('iptables', '-A', 'FORWARD', '-m', 'conntrack', '--ctstate', 'ESTABLISHED,RELATED', '-j', 'ACCEPT')

server = r'''
import socket,threading
s=socket.socket();s.bind(('192.168.52.100',18081));s.listen(8)
def serve(c):
 try:
  c.sendall(('%d\n'%c.getsockopt(socket.IPPROTO_TCP,socket.TCP_MAXSEG)).encode())
  while True:
   b=c.recv(32768)
   if not b:break
   c.sendall(b)
 finally:c.close()
print('READY',flush=True)
while True:
 c,a=s.accept();threading.Thread(target=serve,args=(c,),daemon=True).start()
'''
remote = r'''
import os,fcntl,socket,struct,subprocess,select,threading,sys
tun=os.open('/dev/net/tun',os.O_RDWR)
fcntl.ioctl(tun,0x400454ca,struct.pack('16sH',b'remote0',0x1001))
for args in [('addr','add','198.18.0.7/32','dev','remote0'),('link','set','remote0','mtu','1500','up'),('route','add','192.168.52.0/24','dev','remote0','src','198.18.0.7')]:
 subprocess.check_call(['ip',*args])
carrier=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);carrier.connect(('203.0.113.1',18082))
stop=threading.Event();advertised=[]
def transport():
 while not stop.is_set():
  ready,_,_=select.select([tun,carrier],[],[],.1)
  if tun in ready:carrier.send(b'\x07'+os.read(tun,2048))
  if carrier in ready:
   packet=carrier.recv(2048)
   if packet[9]==6:
    ihl=(packet[0]&15)*4;t=packet[ihl:]
    if t[13]&2:
     opts=t[20:(t[12]>>4)*4];i=0
     while i<len(opts):
      k=opts[i]
      if k==0:break
      if k==1:i+=1;continue
      n=opts[i+1]
      if n<2:break
      if k==2:advertised.append(int.from_bytes(opts[i+2:i+4],'big'))
      i+=n
   os.write(tun,packet)
thread=threading.Thread(target=transport);thread.start()
def connect(mss=0):
 s=socket.socket();s.settimeout(5)
 if mss:s.setsockopt(socket.IPPROTO_TCP,socket.TCP_MAXSEG,mss)
 s.connect(('192.168.52.100',18081));f=s.makefile('rb',buffering=0)
 observed=int(f.readline());assert observed==(mss or 1360),observed
 return s,f
def echo(s,f):
 payload=b'mss-regression-'*18000;s.sendall(payload);data=b''
 while len(data)<len(payload):
  b=f.read(min(32768,len(payload)-len(data)))
  assert b;data+=b
 assert data==payload
try:
 s,f=connect();echo(s,f)
 assert advertised[-1]==1360,advertised
 small,sf=connect(600);echo(small,sf);sf.close();small.close()
 assert advertised[-1]==1360,advertised
 print('PAUSE',flush=True);assert sys.stdin.readline().strip()=='resume'
 echo(s,f);f.close();s.close()
 print('PASS: bidirectional MSS, smaller MSS, large payload and established stream retained',flush=True)
finally:stop.set();thread.join(2);carrier.close();os.close(tun)
'''
target = subprocess.Popen(['ip','netns','exec','mss-lan','python3','-u','-c',server], stdout=subprocess.PIPE, universal_newlines=True)
helper = client = None
try:
    assert line(target)=='READY'
    helper=subprocess.Popen([sys.argv[1],'gateway'],stdout=subprocess.PIPE,universal_newlines=True)
    assert line(helper)=='READY'
    client=subprocess.Popen(['ip','netns','exec','mss-remote','python3','-u','-c',remote],stdin=subprocess.PIPE,stdout=subprocess.PIPE,universal_newlines=True)
    assert line(client)=='PAUSE'
    # Missing rules trigger health detection and repair without reopening TUN.
    run('iptables','-t','mangle','-D','FORWARD','-o','chvr0','-j','CHVR_MSS')
    run('iptables','-t','mangle','-F','CHVR_MSS')
    helper.send_signal(signal.SIGWINCH);assert line(helper)=='REPAIRED'
    original=run('iptables','-t','mangle','-S')
    helper.send_signal(signal.SIGWINCH);assert line(helper)=='REPAIRED'
    assert run('iptables','-t','mangle','-S')==original
    client.stdin.write('resume\n');client.stdin.flush()
    print(line(client));assert client.wait(timeout=10)==0
    # Ordinary TCP forwarding never crosses chvr0 and keeps its MSS.
    direct="import socket;s=socket.create_connection(('192.168.52.100',18081),3);assert int(s.makefile('rb').readline())==1460;s.close()"
    run('ip','netns','exec','mss-remote','python3','-c',direct)
    helper.terminate();assert helper.wait(timeout=10)==0;helper=None
    assert 'CHVR_' not in run('iptables-save')
    print('PASS: MSS repair is idempotent, ordinary forwarding untouched, owned rules removed')
finally:
    for proc in (client,helper,target):
        if proc is not None and proc.poll() is None:
            proc.terminate();proc.wait(timeout=10)
