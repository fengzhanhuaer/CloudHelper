"""Local process/API lifecycle tests; never change router networking."""
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time


def request(path, body):
    raw = json.dumps(body).encode()
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
        sock.settimeout(3)
        sock.connect(str(path))
        sock.sendall(b"POST / HTTP/1.0\r\nContent-Length: " + str(len(raw)).encode() + b"\r\n\r\n" + raw)
        data = bytearray()
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            data.extend(chunk)
        return json.loads(data.split(b"\r\n\r\n", 1)[1])


def start(binary, config, sock):
    proc = subprocess.Popen([binary, "--config", str(config), "--socket", str(sock)], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    for _ in range(100):
        if sock.exists():
            return proc
        if proc.poll() is not None:
            raise AssertionError("service exited before socket readiness")
        time.sleep(.02)
    proc.terminate()
    raise AssertionError("management socket startup timeout")


def main(binary):
    with tempfile.TemporaryDirectory(prefix="chvr-api-") as work:
        root = Path(work)
        config, sock = root / "config.json", root / "admin.sock"
        secret = "test-only-not-a-live-node-key"
        proc = start(binary, config, sock)
        try:
            response = request(sock, {"action": "status"})
            assert response['version']==subprocess.check_output([binary,'--version'],universal_newlines=True).split()[1]
            assert response["ok"] and not response["config"]["enabled"]
            assert response["config"]["port"] == 12040
            assert not response["config"]["allow_wan"]
            files_before=set(root.iterdir())
            logs=request(sock,{"action":"logs"})
            assert logs["ok"] and logs["items"]==[] and set(root.iterdir())==files_before
            assert os.stat(sock).st_mode & 0o777 == 0o600
            assert not request(sock, {"action": "configure", "csrf": "invalid", "config": {"allow_wan": True}})["ok"]
            updated = dict(response["config"], controller="https://controller.test", node_id="7", node_key=secret)
            response = request(sock, {"action": "configure", "csrf": response["csrf"], "config": updated})
            assert response["ok"] and response["key_set"] and secret not in json.dumps(response)
            assert os.stat(config).st_mode & 0o777 == 0o600
            assert json.loads(config.read_text())["node_key"] == secret
            gateway={"enabled":True,"proxy_cidrs":["192.168.50.7/24","10.20.0.0/16"],"allowed_node_ids":["9","14"]}
            response=request(sock,{"action":"configure","csrf":response["csrf"],"config":{"gateway_proxy":gateway}})
            assert response["ok"] and response["config"]["gateway_proxy"]["proxy_cidrs"]==["192.168.50.0/24","10.20.0.0/16"]
            saved=response["config"]["gateway_proxy"]
            invalid=dict(gateway,proxy_cidrs=["172.16.0.0/8"])
            assert not request(sock,{"action":"configure","csrf":response["csrf"],"config":{"gateway_proxy":invalid}})["ok"]
            assert request(sock,{"action":"status"})["config"]["gateway_proxy"]==saved
            csrf = response["csrf"]
            assert not request(sock, {"action": "configure", "csrf": csrf, "config": {"port": 53}})["ok"]
            assert not request(sock, {"action": "measure", "csrf": csrf, "node": "2", "kind": "upload"})["ok"]
            second = subprocess.run([binary, "--config", str(config), "--socket", str(sock)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5)
            assert second.returncode != 0, "duplicate service must not unlink active socket"
            assert request(sock, {"action": "status"})["ok"]
        finally:
            proc.send_signal(signal.SIGTERM)
            stdout, stderr = proc.communicate(timeout=10)
            assert proc.returncode == 0 and not sock.exists()
            assert secret.encode() not in stdout + stderr
        proc = start(binary, config, sock)
        try:
            response = request(sock, {"action": "status"})
            assert response["key_set"] and response["csrf"] != csrf
            assert response["config"]["node_id"] == "7"
            assert response["config"]["gateway_proxy"]==saved
            assert not list(root.glob("*.log"))
        finally:
            proc.terminate()
            proc.communicate(timeout=10)
        print("PASS: native management API, CSRF, redaction, atomic config, restart, singleton, cleanup")


if __name__ == "__main__":
    main(sys.argv[1])
