"""Bounded kernel check in disposable network namespaces, no router or public traffic."""
import os
from pathlib import Path
import socket
import subprocess
import sys
import threading

fixture = str(Path(sys.argv[1]).resolve())
if '--isolated' not in sys.argv:
    if os.environ.get('CHVR_DISPOSABLE_TEST') != '1':
        raise SystemExit('Set CHVR_DISPOSABLE_TEST=1 for isolated kernel validation')
    raise SystemExit(subprocess.call(['unshare', '-n', sys.executable, __file__, fixture, '--isolated']))

def run(*args):
    return subprocess.check_output(args, universal_newlines=True, stderr=subprocess.STDOUT)

def server(ip, port, tcp, reply):
    sock = socket.socket(type=socket.SOCK_STREAM if tcp else socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind((ip, port))
    if tcp:
        sock.listen()
    def connected(peer):
        with peer:
            while True:
                data = peer.recv(256)
                if not data:
                    break
                peer.sendall(reply or data)
    def loop():
        while True:
            if tcp:
                peer, _ = sock.accept()
                threading.Thread(target=connected, args=(peer,), daemon=True).start()
            else:
                data, peer = sock.recvfrom(256)
                sock.sendto(reply or data, peer)
    threading.Thread(target=loop, daemon=True).start()

client_source = r'''
import socket,subprocess,sys
print('NAMESPACE',flush=True)
assert input()=='setup'
for args in [('ip','link','set','lo','up'),('ip','link','set','client0','up'),('ip','addr','add','192.0.2.2/24','dev','client0'),('ip','route','add','default','via','192.0.2.1')]:subprocess.check_call(args)
def call(ip,port,tcp,expected):
    with socket.socket(type=socket.SOCK_STREAM if tcp else socket.SOCK_DGRAM) as s:
        s.settimeout(.3)
        try:
            s.connect((ip,port));s.sendall(b'probe');data=s.recv(256)
        except (TimeoutError,ConnectionError):data=None
        assert data==expected,(ip,port,tcp,data,expected)
held=socket.create_connection(('203.0.113.54',443),1)
print('READY',flush=True)
for command in sys.stdin:
    if command.strip()=='stop':break
    on=command.strip()=='on'
    for tcp in (True,False):
        call('203.0.113.53',443,tcp,None if on else b'probe')
        call('203.0.113.53',853,tcp,None if on else b'probe')
        call('203.0.113.53',53,tcp,b'localDNS' if on else b'externalDNS')
    call('203.0.113.54',443,True,b'probe')
    call('203.0.113.53',80,True,b'probe')
    held.sendall(b'established');assert held.recv(256)==b'established'
    print('PASS',flush=True)
held.close()
'''
client = subprocess.Popen(['unshare', '-n', sys.executable, '-u', '-c', client_source], stdin=subprocess.PIPE, stdout=subprocess.PIPE, universal_newlines=True)
router = None
try:
    assert client.stdout.readline().strip() == 'NAMESPACE'
    run('ip', 'link', 'set', 'lo', 'up')
    run('ip', 'link', 'add', 'lan0', 'type', 'veth', 'peer', 'name', 'client0')
    run('ip', 'link', 'set', 'client0', 'netns', str(client.pid))
    run('ip', 'addr', 'add', '192.0.2.1/24', 'dev', 'lan0')
    run('ip', 'link', 'set', 'lan0', 'up')
    for ip in ('203.0.113.53', '203.0.113.54'):
        run('ip', 'addr', 'add', ip+'/32', 'dev', 'lo')
    for port in (443, 853, 80):
        server('203.0.113.53', port, True, None)
    server('203.0.113.54', 443, True, None)
    for port in (443, 853):
        server('203.0.113.53', port, False, None)
    for tcp in (True, False):
        server('192.0.2.1', 53, tcp, b'localDNS')
        server('203.0.113.53', 53, tcp, b'externalDNS')
    run('iptables', '-A', 'INPUT', '-s', '198.51.100.0/24', '-j', 'DROP')
    client.stdin.write('setup\n');client.stdin.flush()
    assert client.stdout.readline().strip() == 'READY'
    router = subprocess.Popen([fixture], stdin=subprocess.PIPE, stdout=subprocess.PIPE, universal_newlines=True)
    assert router.stdout.readline().strip() == 'READY'
    for mode in ('off', 'on', 'off'):
        router.stdin.write(mode+'\n');router.stdin.flush()
        assert router.stdout.readline().strip() == 'APPLIED'
        # Router-originated control and upstream sockets bypass LAN interception.
        with socket.create_connection(('203.0.113.53', 443), 1) as s:
            s.sendall(b'upstream');assert s.recv(256) == b'upstream'
        client.stdin.write(mode+'\n');client.stdin.flush()
        assert client.stdout.readline().strip() == 'PASS'
    router.stdin.write('stop\n');router.stdin.flush();assert router.wait(timeout=5) == 0
    assert 'CHVR_DNS' not in run('iptables-save')
    run('iptables', '-C', 'INPUT', '-s', '198.51.100.0/24', '-j', 'DROP')
    client.stdin.write('stop\n');client.stdin.flush();assert client.wait(timeout=5) == 0
    print('PASS: LAN DoH/DoT block, TCP/UDP DNS redirect, unaffected OUTPUT/ordinary HTTPS, established stream, disable and owned-rule cleanup')
finally:
    for process in (router, client):
        if process and process.poll() is None:
            process.kill();process.wait()
