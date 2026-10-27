"""Exercise the production route command diff, including failed-update rollback."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

with tempfile.TemporaryDirectory(prefix='chvr-routes-') as work:
    root = Path(work)
    table = root/'table.json'
    commands = root/'commands.json'
    fake_ip = root/'ip'
    fake_ip.write_text('''#!/usr/bin/python3
import json, os, sys
from pathlib import Path
table, log = Path(os.environ['CHVR_ROUTE_TABLE']), Path(os.environ['CHVR_ROUTE_LOG'])
a = sys.argv[1:]
assert a[:1] == ['route'] and a[1] in ['add', 'del'] and a[3:] in [['dev', 'chvr0', 'proto', '186'], ['dev', 'chvr0', 'table', '187', 'proto', '186']], a
events = json.loads(log.read_text());events.append(a);log.write_text(json.dumps(events))
rows = json.loads(table.read_text())
if ':'.join(a[1:3]) == os.environ.get('CHVR_FAIL_ROUTE'):sys.exit(1)
key = ('187:' if 'table' in a else '')+a[2]
if a[1] == 'add':
 if key in rows:sys.exit(1)
 rows.append(key)
else:
 if key not in rows:sys.exit(1)
 rows.remove(key)
table.write_text(json.dumps(rows))
''')
    fake_ip.chmod(0o700)
    def policy(cidrs):
        return {'route_rules': [{'id': 'gateway-proxy-fixture', 'action': 'probe_exit',
                                 'entries': ['cidr:'+c for c in cidrs]}]}
    a, b, c, d = ['192.168.'+str(i)+'.0/24' for i in range(221, 225)]
    old, new = root/'old.json', root/'new.json'
    old.write_text(json.dumps(policy([a, b])))
    new.write_text(json.dumps(policy([b, c, d])))
    preserved = ['198.18.0.0/15', 'unrelated-route']
    env = dict(os.environ, PATH=str(root)+':'+os.environ['PATH'], CHVR_ROUTE_TABLE=str(table), CHVR_ROUTE_LOG=str(commands))
    for fail in ['', 'add:'+d, 'del:'+a]:
        table.write_text(json.dumps(preserved+[a, b]));commands.write_text('[]')
        result = subprocess.run(['bin/native/test-routes', str(old), str(new)], env=dict(env, CHVR_FAIL_ROUTE=fail))
        assert result.returncode == (1 if fail else 0)
        assert sorted(json.loads(table.read_text())) == sorted(preserved+([a, b] if fail else [b, c, d]))
        assert all(event[2] != b for event in json.loads(commands.read_text()))
    commands.write_text('[]')
    assert subprocess.call(['bin/native/test-routes', str(old), str(old)], env=env) == 0
    assert not json.loads(commands.read_text())
    old.write_text(json.dumps({'route_rules': []}))
    new.write_text(json.dumps({'route_rules': [
        {'id':'empty-missing','action':'probe_exit','exit_node_id':'8'},
        {'id':'empty-null','action':'reject','entries':None},
        {'id':'empty-array','action':'probe_exit','exit_node_id':'8','entries':[]},
        {'id':'ordinary-cidr','action':'probe_exit','exit_node_id':'8','entries':['cidr:203.0.113.199/24','ip_cidr:198.51.100.0/24','ip-cidr:0.0.0.0/0','ip:192.0.2.8']},
        {'id':'ordinary-reject','action':'reject','entries':['cidr:192.0.2.0/24']},
        {'id':'disabled','enabled':False,'action':'probe_exit','entries':['cidr:8.8.8.0/24']},
        {'id':'direct','action':'direct','entries':['cidr:9.9.9.0/24']},
    ]}))
    table.write_text(json.dumps(preserved));commands.write_text('[]')
    assert subprocess.call(['bin/native/test-routes', str(old), str(new)], env=env) == 0
    assert sorted(json.loads(table.read_text())) == sorted(preserved+['187:'+c for c in ['203.0.113.0/24','198.51.100.0/24','0.0.0.0/0','192.0.2.8/32','192.0.2.0/24']])
    assert subprocess.call(['bin/native/test-routes', str(new), str(old)], env=env) == 0
    assert sorted(json.loads(table.read_text())) == sorted(preserved)
    print('PASS: private-route add/remove diff, unchanged routes, and partial-add/delete rollback')
    print('PASS: ordinary proxy/reject CIDRs, aliases, canonicalization, default rule and LAN-only table 187')
