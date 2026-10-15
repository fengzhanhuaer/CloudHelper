"""Real kernel TUN/NAT tests, exclusively inside a disposable network container."""
import os
from pathlib import Path
import subprocess
import sys

if os.environ.get('CHVR_DISPOSABLE_TEST') != '1' or not Path('/.dockerenv').exists():
    raise SystemExit('Run only inside the disposable test container')

def run(*args):
    try:
        return subprocess.check_output(args, stderr=subprocess.STDOUT, universal_newlines=True)
    except subprocess.CalledProcessError as error:
        print(error.output, file=sys.stderr)
        raise

for ns, interface, router_ip, peer_ip in [
    ('chvr-client', 'chvrlan', '192.168.52.1', '192.168.52.100'),
    ('chvr-target', 'chvrwan', '203.0.113.1', '203.0.113.5'),
]:
    run('ip', 'netns', 'add', ns)
    run('ip', 'link', 'add', interface, 'type', 'veth', 'peer', 'name', interface+'p')
    run('ip', 'link', 'set', interface+'p', 'netns', ns)
    run('ip', 'addr', 'add', router_ip+'/24', 'dev', interface)
    run('ip', 'link', 'set', interface, 'up')
    run('ip', '-n', ns, 'addr', 'add', peer_ip+'/24', 'dev', interface+'p')
    run('ip', '-n', ns, 'link', 'set', interface+'p', 'up')
    run('ip', '-n', ns, 'link', 'set', 'lo', 'up')
    run('ip', '-n', ns, 'route', 'add', 'default', 'via', router_ip)
Path('/proc/sys/net/ipv4/ip_forward').write_text('1')
run('iptables', '-P', 'FORWARD', 'DROP')
run('iptables', '-A', 'INPUT', '-m', 'comment', '--comment', 'preserve-fixture', '-j', 'ACCEPT')

server = '''import socket,threading
def serve(tcp):
 s=socket.socket(socket.AF_INET,socket.SOCK_STREAM if tcp else socket.SOCK_DGRAM)
 s.bind(('203.0.113.5',18080))
 if tcp:
  s.listen(4)
  while True:
   c,a=s.accept();b=c.recv(1024);c.sendall(b);c.close()
 else:
  while True:
   b,a=s.recvfrom(1024);s.sendto(b,a)
threading.Thread(target=serve,args=(False,),daemon=True).start()
serve(True)
'''
target = subprocess.Popen(['ip','netns','exec','chvr-target','python3','-u','-c',server])
try:
    for allow_wan in (0,1):
        helper = subprocess.Popen([sys.argv[1],str(allow_wan)],stdout=subprocess.PIPE,universal_newlines=True)
        try:
            assert helper.stdout.readline().strip() == 'READY'
            for tcp in (0,1):
                client = '''import socket
s=socket.socket(socket.AF_INET,socket.SOCK_STREAM if %d else socket.SOCK_DGRAM)
s.settimeout(4);s.connect(('198.18.0.20',18080));s.sendall(b'kernel-nat-roundtrip')
assert s.recv(1024)==b'kernel-nat-roundtrip'
''' % tcp
                run('ip','netns','exec','chvr-client','python3','-c',client)
            for ns,ip,expected in [('chvr-client','192.168.52.1',True),('chvr-target','203.0.113.1',bool(allow_wan))]:
                client='import socket; s=socket.socket(); s.settimeout(1); assert (s.connect_ex((%r,12040))==0)==%r' % (ip,expected)
                run('ip','netns','exec',ns,'python3','-c',client)
            print('PASS: kernel TCP/UDP fake-IP NAT and LAN/WAN listener policy allow_wan='+str(allow_wan),flush=True)
        finally:
            helper.terminate()
            helper.wait(timeout=20)
        assert 'CHVR_' not in run('iptables-save')
        assert 'preserve-fixture' in run('iptables-save')
        assert 'chvr0' not in run('ip','link','show')
        assert 'chvr0' not in run('ip','rule','show')
    print('PASS: owned firewall/TUN cleanup preserves unrelated rules')
finally:
    target.terminate()
    target.wait(timeout=5)
