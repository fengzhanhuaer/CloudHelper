"""Real kernel TUN/NAT tests, exclusively inside a disposable network container."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import signal

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
if os.environ.get('CHVR_STRICT_RPF') == '1':
    Path('/proc/sys/net/ipv4/conf/all/rp_filter').write_text('1')
    Path('/proc/sys/net/ipv4/conf/default/rp_filter').write_text('1')
run('iptables', '-P', 'FORWARD', 'DROP')
run('iptables','-A','FORWARD','-m','conntrack','--ctstate','ESTABLISHED,RELATED','-j','ACCEPT')
run('iptables','-A','FORWARD','-i','chvrlan','-o','chvrwan','-p','tcp','--dport','443','-j','ACCEPT')
run('iptables', '-A', 'INPUT', '-m', 'comment', '--comment', 'preserve-fixture', '-j', 'ACCEPT')

server = '''import socket,threading
def serve(tcp):
 s=socket.socket(socket.AF_INET,socket.SOCK_STREAM if tcp else socket.SOCK_DGRAM)
 s.bind(('203.0.113.5',18080))
 if tcp:
  s.listen(4)
  def echo(c):
   try:
    while True:
     b=c.recv(4096)
     if not b:break
     c.sendall(b)
   finally:c.close()
  while True:
   c,a=s.accept();threading.Thread(target=echo,args=(c,),daemon=True).start()
 else:
  while True:
   b,a=s.recvfrom(1024);s.sendto(b,a)
threading.Thread(target=serve,args=(False,),daemon=True).start()
serve(True)
'''
target = subprocess.Popen(['ip','netns','exec','chvr-target','python3','-u','-c',server])
target2 = None
try:
    for allow_wan in (0,1):
        helper = subprocess.Popen([sys.argv[1],str(allow_wan)],stdout=subprocess.PIPE,universal_newlines=True)
        try:
            line=helper.stdout.readline().strip()
            assert line == 'READY', (line,helper.poll())
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
    # Ordinary CIDR is deliberately behind the physical WAN next hop. A
    # connected /24 would correctly keep using the original management LAN.
    run('ip','-n','chvr-target','addr','add','198.51.100.5/32','dev','chvrwanp')
    run('ip','route','add','198.51.100.0/24','via','203.0.113.5','dev','chvrwan')
    # Bind replies to the tested destination; an INADDR_ANY UDP echo would
    # reply from 203.0.113.5 and fail conntrack's reverse tuple by construction.
    target2 = subprocess.Popen(['ip','netns','exec','chvr-target','python3','-u','-c',server.replace('203.0.113.5','198.51.100.5')])
    with tempfile.TemporaryDirectory(prefix='chvr-kernel-tls-') as work:
        cert,key=work+'/cert.pem',work+'/key.pem'
        run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',key,'-out',cert,'-days','1','-subj','/CN=fixture.example')
        tls_server='''import socket,ssl,threading
c=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);c.load_cert_chain(%r,%r)
s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('203.0.113.5',443));s.listen(8)
def serve(fd):
 try:
  with c.wrap_socket(fd,server_side=True) as peer:
   while True:
    b=peer.recv(4096)
    if not b:break
    peer.sendall(b)
 except (OSError,ssl.SSLError):fd.close()
print('READY',flush=True)
while True:
 fd,a=s.accept();threading.Thread(target=serve,args=(fd,),daemon=True).start()
''' % (cert,key)
        tls = subprocess.Popen(['ip','netns','exec','chvr-target','python3','-u','-c',tls_server],stdout=subprocess.PIPE,universal_newlines=True)
        helper = None
        try:
            assert tls.stdout.readline().strip() == 'READY'
            helper = subprocess.Popen([sys.argv[1],'routing'],stdout=subprocess.PIPE,universal_newlines=True)
            assert helper.stdout.readline().strip() == 'READY'
            assert 'chvr0' in run('ip','route','get','198.51.100.5','from','192.168.52.100','iif','chvrlan')
            assert 'chvr0' not in run('ip','route','get','198.51.100.5')
            for tcp in (0,1):
                client='''import socket
s=socket.socket(socket.AF_INET,socket.SOCK_STREAM if %d else socket.SOCK_DGRAM)
s.settimeout(4);s.connect(('198.51.100.5',18080));s.sendall(b'ordinary-cidr');assert s.recv(1024)==b'ordinary-cidr'
''' % tcp
                run('ip','netns','exec','chvr-client','python3','-c',client)
            persistent='''import socket,sys
s=socket.create_connection(('198.51.100.5',18080),8);s.sendall(b'before');assert s.recv(1024)==b'before'
print('CONNECTED',flush=True);sys.stdin.readline();s.sendall(b'after');assert s.recv(1024)==b'after'
print('RETAINED',flush=True);s.close()
'''
            live=subprocess.Popen(['ip','netns','exec','chvr-client','python3','-u','-c',persistent],stdin=subprocess.PIPE,stdout=subprocess.PIPE,universal_newlines=True)
            try:
                assert live.stdout.readline().strip()=='CONNECTED'
                run('ip','route','del','198.51.100.0/24','dev','chvr0','table','187','proto','186')
                assert 'chvr0' in run('ip','route','get','198.51.100.5','from','192.168.52.100','iif','chvrlan','mark','0x43000000')
                assert 'chvr0' not in run('ip','route','get','198.51.100.5','from','192.168.52.100','iif','chvrlan')
                live.stdin.write('\n');live.stdin.flush();assert live.stdout.readline().strip()=='RETAINED';assert live.wait(timeout=5)==0
            except Exception:
                print(run('iptables-save','-c'),file=sys.stderr)
                print(run('ip','rule','show'),file=sys.stderr)
                for interface in ['all','chvr0','chvrlan','chvrwan']:
                    print(interface,Path('/proc/sys/net/ipv4/conf/'+interface+'/rp_filter').read_text(),file=sys.stderr)
                for path in ['/proc/net/nf_conntrack','/proc/net/ip_conntrack']:
                    if Path(path).exists():print(Path(path).read_text(),file=sys.stderr)
                raise
            finally:
                if live.poll() is None:live.terminate();live.wait(timeout=5)
            print('PASS: existing marked TCP survives CIDR removal; new connections and local sockets use current route',flush=True)
            def persistent_tls(host):
                source='''import socket,ssl,sys
s=ssl._create_unverified_context().wrap_socket(socket.create_connection(('203.0.113.5',443),8),server_hostname=%r)
s.sendall(b'before');assert s.recv(1024)==b'before';print('CONNECTED',flush=True)
sys.stdin.readline();s.sendall(b'after');assert s.recv(1024)==b'after';print('RETAINED',flush=True);s.close()
''' % host
                live=subprocess.Popen(['ip','netns','exec','chvr-client','python3','-u','-c',source],stdin=subprocess.PIPE,stdout=subprocess.PIPE,universal_newlines=True)
                assert live.stdout.readline().strip()=='CONNECTED'
                return live
            def finish_tls(live):
                try:
                    live.stdin.write('\n');live.stdin.flush();assert live.stdout.readline().strip()=='RETAINED';assert live.wait(timeout=5)==0
                finally:
                    if live.poll() is None:live.terminate();live.wait(timeout=5)
            # Enabling sniffing never intercepts an established direct TLS flow.
            direct=persistent_tls('blocked.example')
            helper.send_signal(signal.SIGUSR1);assert helper.stdout.readline().strip()=='TLS_ON'
            finish_tls(direct)
            for host,allowed in [('fixture.example',True),('blocked.example',False)]:
                client='''import socket,ssl
c=ssl._create_unverified_context()
try:
 s=c.wrap_socket(socket.create_connection(('203.0.113.5',443),4),server_hostname=%r)
 s.sendall(b'encrypted-roundtrip');assert s.recv(4096)==b'encrypted-roundtrip';s.close()
except (OSError,ssl.SSLError):
 assert not %r
else:
 assert %r
''' % (host,allowed,allowed)
                run('ip','netns','exec','chvr-client','python3','-c',client)
            proxied=persistent_tls('fixture.example')
            helper.send_signal(signal.SIGUSR1);assert helper.stdout.readline().strip()=='TLS_OFF'
            finish_tls(proxied)
            print('PASS: SNI enable retains existing direct TLS, disable retains existing sniffed TLS',flush=True)
            print('PASS: real LAN-only CIDR TCP/UDP route, physical exit without loop, local sockets bypass, transparent TLS SNI direct/reject',flush=True)
        finally:
            if helper is not None:helper.terminate();helper.wait(timeout=20)
            tls.terminate();tls.wait(timeout=5)
    assert 'CHVR_' not in run('iptables-save')
    assert 'preserve-fixture' in run('iptables-save')
    assert 'chvr0' not in run('ip','link','show')
    assert '18601' not in run('ip','rule','show')
    print('PASS: owned firewall/TUN cleanup preserves unrelated rules')
finally:
    if target2 is not None:target2.terminate();target2.wait(timeout=5)
    target.terminate()
    target.wait(timeout=5)
