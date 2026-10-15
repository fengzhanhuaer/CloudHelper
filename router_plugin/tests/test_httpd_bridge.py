"""Real socket relay, inherited authentication, CSRF and stock delegation."""
import http.client
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time

root=Path(__file__).resolve().parents[1]
def call(port,path,body,authenticated=True):
    connection=http.client.HTTPConnection('127.0.0.1',port,timeout=5)
    try:
        headers={'Cookie':'stock_session=fixture'} if authenticated else {}
        connection.request('POST','/'+path,body=body,headers=headers)
        response=connection.getresponse()
        return response.status,response.read()
    finally:connection.close()

with tempfile.TemporaryDirectory(prefix='chvr-httpd-') as directory:
    runtime=Path(directory);sock=runtime/'admin.sock'
    subprocess.check_call(['cc','-Os','-std=c99','-Wall','-Wextra','-Werror','-fPIC','-shared',
        '-DCHVR_ADMIN_SOCKET="'+str(sock)+'"',str(root/'src/httpd_bridge.c'),'-ldl','-o',str(runtime/'bridge.so')])
    subprocess.check_call(['cc','-Os','-std=c99','-Wall','-Wextra','-Werror','-rdynamic',str(root/'tests/httpd_fixture.c'),'-o',str(runtime/'httpd')])
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
    finally:
        if web:web.terminate();web.wait(timeout=5)
        daemon.terminate();daemon.wait(timeout=10)
print('PASS: stock auth callback retained, Unix API relay, CSRF/config, bounded malformed requests, stock-page delegation')
