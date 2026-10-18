import pathlib
import subprocess
import tempfile

with tempfile.TemporaryDirectory(prefix='chvr-tx-') as directory:
    path = pathlib.Path(directory)
    cert, key = path / 'cert.pem', path / 'key.pem'
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                    '-keyout', str(key), '-out', str(cert), '-days', '1',
                    '-subj', '/CN=chvr-tx-fixture'], check=True,
                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    subprocess.run(['bin/native/test-tx', str(cert), str(key)], check=True, timeout=20)
