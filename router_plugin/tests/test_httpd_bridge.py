"""Real socket relay, inherited authentication, CSRF and stock delegation."""
import http.client
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import ssl

root=Path(__file__).resolve().parents[1]
def call(port,path,body,authenticated=True,tls=False,source=None):
    options={'timeout':5,'source_address':(source,0) if source else None}
    connection=(http.client.HTTPSConnection('127.0.0.1',port,context=ssl._create_unverified_context(),**options) if tls else http.client.HTTPConnection('127.0.0.1',port,**options))
    try:
        headers={'Cookie':'stock_session=fixture'} if authenticated else {}
        connection.request('POST','/'+path,body=body,headers=headers)
        response=connection.getresponse()
        return response.status,response.read()
    finally:connection.close()

def gateway_calls(port,api,tls=False):
    # Optional isolated network namespace: give chvr0 198.18.0.8/15 and a
    # different interface 198.20.0.8/24. No production route/auth mocks.
    if os.environ.get('CHVR_TEST_GATEWAY_NETWORK')!='1':return
    assert call(port,api,b'{"action":"status"}',False,tls,'198.18.0.8')[0]==401
    assert json.loads(call(port,api,b'{"action":"status"}',tls=tls,source='198.18.0.8')[1])['ok']
    assert not json.loads(call(port,api,b'{"action":"configure","csrf":"bad","config":{}}',tls=tls,source='198.18.0.8')[1])['ok']
    assert not json.loads(call(port,api,b'{"action":"status"}',tls=tls,source='198.20.0.8')[1])['ok']

with tempfile.TemporaryDirectory(prefix='chvr-httpd-') as directory:
    runtime=Path(directory);sock=runtime/'admin.sock'
    subprocess.check_call(['cc','-Os','-std=c99','-Wall','-Wextra','-Werror','-fPIC','-shared',
        '-DCHVR_ADMIN_SOCKET="'+str(sock)+'"',str(root/'src/httpd_bridge.c'),'-ldl','-o',str(runtime/'bridge.so')])
    for hidden in [False,True]:
        subprocess.check_call(['cc','-Os','-std=c99','-Wall','-Wextra','-Werror','-no-pie','-rdynamic']+(['-DCHVR_HIDDEN'] if hidden else [])+[str(root/'tests/httpd_fixture.c'),'-lssl','-lcrypto','-o',str(runtime/('hidden-httpd' if hidden else 'httpd'))])
    daemon=subprocess.Popen([str(root/'bin/native/cloudhelper-router-plugin'),'--config',str(runtime/'config.json'),'--socket',str(sock)])
    web=None
    try:
        for _ in range(100):
            if sock.exists():break
            time.sleep(.05)
        assert sock.exists()
        web=subprocess.Popen([str(runtime/'httpd')],env={**os.environ,'LD_PRELOAD':str(runtime/'bridge.so')},stdout=subprocess.PIPE,universal_newlines=True)
        port=int(web.stdout.readline())
        api='user/cloudhelper-router/api.asp'
        gateway_calls(port,api)
        assert call(port,api,b'{"action":"status"}',False)[0]==401
        code,raw=call(port,api,b'{"action":"status"}')
        status=json.loads(raw);assert code==200 and status['ok'] and len(status['csrf'])==48
        assert not json.loads(call(port,api,b'{"action":"configure","csrf":"bad","config":{}}')[1])['ok']
        config=status['config'];config['port']=12041
        assert json.loads(call(port,api,json.dumps({'action':'configure','csrf':status['csrf'],'config':config}))[1])['ok']
        assert json.loads(call(port,api,b'{"action":"status"}')[1])['config']['port']==12041
        assert b'stock page'==call(port,'Main_Netstat.asp',b'')[1]
        assert b'stock page'==call(port,api+'?unexpected=1',b'')[1]
        assert not json.loads(call(port,api,b'x'*16385)[1])['ok']
        assert not json.loads(call(port,api,b'')[1])['ok']
        # The service remains live after malformed requests; no port is opened
        # by the daemon or the extension (only fixture's stock-port listener).
        assert json.loads(call(port,api,b'{"action":"status"}')[1])['ok']
        web.terminate();web.wait(timeout=5);web=None
        subprocess.check_call(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-keyout',str(runtime/'key.pem'),'-out',str(runtime/'cert.pem'),'-subj','/CN=localhost'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        for tls in [False,True]:
            arguments=[str(runtime/'hidden-httpd')]+(['--tls',str(runtime/'cert.pem'),str(runtime/'key.pem')] if tls else [])
            web=subprocess.Popen(arguments,env={**os.environ,'LD_PRELOAD':str(runtime/'bridge.so')},stdout=subprocess.PIPE,universal_newlines=True)
            port=int(web.stdout.readline())
            gateway_calls(port,api,tls)
            assert call(port,api,b'{"action":"status"}',False,tls)[0]==401
            assert json.loads(call(port,api,b'{"action":"status"}',tls=tls)[1])['ok']
            assert call(port,'Main_Netstat.asp',b'',tls=tls)[1]==b'stock page'
            web.terminate();web.wait(timeout=5);web=None
    finally:
        if web:web.terminate();web.wait(timeout=5)
        daemon.terminate();daemon.wait(timeout=10)
print('PASS: stock auth callback retained, Unix API relay, CSRF/config, bounded malformed requests, stock-page delegation, gateway network='+os.environ.get('CHVR_TEST_GATEWAY_NETWORK','0'))
