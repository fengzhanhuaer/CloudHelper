"""Real listeners, conntrack and TLS streams survive address/interface updates."""
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile

if os.environ.get('CHVR_DISPOSABLE_TEST') != '1' or not Path('/.dockerenv').exists():
    raise SystemExit('Run only inside the disposable network container')

def run(*args):
    return subprocess.check_output(args, stderr=subprocess.STDOUT, universal_newlines=True)

def line(proc):
    assert select.select([proc.stdout], [], [], 15)[0], ('timeout', proc.poll())
    return proc.stdout.readline().strip()

def state():
    return sorted(s for s in run('iptables-save').splitlines() if s.startswith('-A ')), sorted(run('ip', 'rule', 'show').splitlines())

def unchanged(original):
    current=state()
    if current!=original:
        for before,after in zip(original,current):
            print('REMOVED',sorted(set(before)-set(after)),file=sys.stderr)
            print('ADDED',sorted(set(after)-set(before)),file=sys.stderr)
    assert current==original

for ns, interface, router, peer in [('chvr-client','chvrlan','192.168.52','192.168.52.100'), ('chvr-target','chvrwan','203.0.113','203.0.113.5')]:
    run('ip','netns','add',ns)
    run('ip','link','add',interface,'type','veth','peer','name',interface+'p')
    run('ip','link','set',interface+'p','netns',ns)
    for suffix in (1,2):run('ip','addr','add',router+'.'+str(suffix)+'/24','dev',interface)
    run('ip','link','set',interface,'up')
    run('ip','-n',ns,'addr','add',peer+'/24','dev',interface+'p')
    run('ip','-n',ns,'link','set',interface+'p','up')
    run('ip','-n',ns,'link','set','lo','up')
    run('ip','-n',ns,'route','add','default','via',router+'.1')
Path('/proc/sys/net/ipv4/ip_forward').write_text('1')
run('iptables','-P','INPUT','DROP')
run('iptables','-A','INPUT','-m','conntrack','--ctstate','ESTABLISHED,RELATED','-j','ACCEPT')
run('iptables','-P','FORWARD','DROP')
run('iptables','-A','FORWARD','-m','conntrack','--ctstate','ESTABLISHED,RELATED','-j','ACCEPT')
run('iptables','-A','FORWARD','-i','chvrlan','-o','chvrwan','-p','tcp','--dport','443','-j','ACCEPT')

with tempfile.TemporaryDirectory(prefix='chvr-addresses-') as work:
    cert,key=work+'/cert.pem',work+'/key.pem'
    run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',key,'-out',cert,'-days','1','-subj','/CN=fixture.example')
    source='''import socket,ssl,threading
c=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);c.load_cert_chain(%r,%r)
def serve(fd,tls):
 try:
  if tls:fd=c.wrap_socket(fd,server_side=True)
  while True:
   b=fd.recv(4096)
   if not b:break
   fd.sendall(b)
 except (OSError,ssl.SSLError):pass
 finally:fd.close()
def listen(port):
 s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('203.0.113.5',port));s.listen(8)
 while True:
  fd,_=s.accept();threading.Thread(target=serve,args=(fd,port==443),daemon=True).start()
threading.Thread(target=listen,args=(18080,),daemon=True).start()
print('READY',flush=True);listen(443)
''' % (cert,key)
    server=subprocess.Popen(['ip','netns','exec','chvr-target','python3','-u','-c',source],stdout=subprocess.PIPE,universal_newlines=True)
    helper=None;streams=[]
    try:
        assert line(server)=='READY'
        env=dict(os.environ,CHVR_PLATFORM_FAIL_FILE=work+'/fail',CHVR_CHECK_FAIL_FILE=work+'/check-fail')
        helper=subprocess.Popen([sys.argv[1],'readdress'],env=env,stdout=subprocess.PIPE,universal_newlines=True)
        assert line(helper)=='READY'
        def client(ns,host,port,tls=False):
            source='''import socket,ssl,sys
s=socket.create_connection((%r,%d),6);s.settimeout(6)
if %r:s=ssl._create_unverified_context().wrap_socket(s,server_hostname='fixture.example')
print('READY',flush=True)
for text in sys.stdin:
 b=text.encode();s.sendall(b);assert s.recv(4096)==b;print('OK',flush=True)
s.close()
''' % (host,port,tls)
            args=['ip','netns','exec',ns] if ns else []
            proc=subprocess.Popen(args+['python3','-u','-c',source],stdin=subprocess.PIPE,stdout=subprocess.PIPE,universal_newlines=True)
            streams.append(proc);assert line(proc)=='READY';return proc
        client('chvr-client','192.168.52.1',12040)
        client('chvr-target','203.0.113.1',12040)
        client(None,'203.0.113.5',18080)  # Outbound controller-shaped connection.
        client('chvr-client','203.0.113.5',443,True)
        def retained():
            for proc in streams:
                proc.stdin.write('still-connected\n');proc.stdin.flush();assert line(proc)=='OK'
        retained();original=state()
        Path(env['CHVR_CHECK_FAIL_FILE']).touch()
        helper.send_signal(signal.SIGWINCH);assert line(helper)=='REPAIR_DEFERRED'
        unchanged(original);retained()
        Path(env['CHVR_CHECK_FAIL_FILE']).unlink()
        print('PASS: transient iptables exit 4 reports exact check, preserves rules and established streams',flush=True)
        # A partial external change is repaired without flushing live flows.
        run('iptables','-t','nat','-D','PREROUTING','-i','chvrlan','-p','tcp','--dport','443','-j','CHVR_TLS')
        run('iptables','-t','nat','-D','CHVR_TLS','-d','192.168.0.0/16','-j','RETURN')
        helper.send_signal(signal.SIGWINCH);assert line(helper)=='REPAIRED'
        unchanged(original);retained()
        tls_rules=run('iptables','-t','nat','-S','CHVR_TLS').splitlines()
        assert next(i for i,s in enumerate(tls_rules) if '192.168.0.0/16' in s)<next(i for i,s in enumerate(tls_rules) if '-j DNAT' in s)
        # Emulate firmware resetting only the plugin-owned firewall chains.
        table='filter'
        for text in run('iptables-save').splitlines():
            if text.startswith('*'):table=text[1:]
            if text.startswith('-A ') and '-j CHVR_' in text and not text.split()[1].startswith('CHVR_'):
                parts=text.split();parts[0]='-D';run('iptables','-t',table,*parts)
        chains={'filter':['CHVR_IN','CHVR_FWD'],'nat':['CHVR_NAT','CHVR_TLS'],'mangle':['CHVR_FLOW','CHVR_MARK']}
        for table,names in chains.items():
            for name in names:run('iptables','-t',table,'-F',name)
            for name in names:run('iptables','-t',table,'-X',name)
        helper.send_signal(signal.SIGWINCH);assert line(helper)=='REPAIRED'
        unchanged(original);retained()
        repaired=state()
        helper.send_signal(signal.SIGWINCH);assert line(helper)=='REPAIRED'
        unchanged(repaired);retained()
        new=client('chvr-client','203.0.113.5',443,True)
        new.stdin.write('repaired-sni\n');new.stdin.flush();assert line(new)=='OK'
        new.stdin.close();assert new.wait(timeout=5)==0;streams.remove(new)
        print('PASS: partial and full owned-firewall loss repair in place; existing carriers/control/TLS and new SNI work, repeated repair adds no duplicates',flush=True)
        helper.send_signal(signal.SIGALRM);assert line(helper)=='REFRESH_FAILED'
        unchanged(original);retained()
        print('PASS: unavailable replacement address retains listeners, firewall and established streams',flush=True)
        current=False
        for iteration in range(6):
            target=not current
            for name in ('chvrlan','chvrwan'):
                run('ip','link','set',name+('2' if current else ''),'name',name+('2' if target else ''))
            if iteration==0:
                # The kernel labels old iif rules detached after a physical
                # interface rename, independently of the plugin transaction.
                renamed=state()
                Path(env['CHVR_PLATFORM_FAIL_FILE']).touch()
                helper.send_signal(signal.SIGUSR2);assert line(helper)=='REFRESH_FAILED'
                assert not Path(env['CHVR_PLATFORM_FAIL_FILE']).exists()
                unchanged(renamed);retained()
                print('PASS: mid-update firewall deletion failure rolls back added and removed rules, keeps streams',flush=True)
            helper.send_signal(signal.SIGUSR2);assert line(helper)=='UPDATED'
            current=target;retained()
            for ns,host in [('chvr-client','192.168.52.'),('chvr-target','203.0.113.')]:
                proc=client(ns,host+('2' if current else '1'),12040)
                proc.stdin.write('new-listener\n');proc.stdin.flush();assert line(proc)=='OK'
                proc.stdin.close();assert proc.wait(timeout=5)==0;streams.remove(proc)
            proc=client('chvr-client','203.0.113.5',443,True)
            proc.stdin.write('new-sni-listener\n');proc.stdin.flush();assert line(proc)=='OK'
            proc.stdin.close();assert proc.wait(timeout=5)==0;streams.remove(proc)
            if not current:unchanged(original)
        print('PASS: six LAN/WAN IP and interface migrations preserve inbound carriers, outbound control and SNI TLS; new listeners work, no rule accumulation',flush=True)
    finally:
        for proc in streams:
            if proc.poll() is None:proc.terminate()
            proc.wait(timeout=10)
        if helper is not None:
            helper.terminate();assert helper.wait(timeout=20)==0
            assert 'CHVR_' not in run('iptables-save')
        server.terminate();server.wait(timeout=10)
