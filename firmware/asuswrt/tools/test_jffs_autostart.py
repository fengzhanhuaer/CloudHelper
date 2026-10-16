"""Run the real startup shell in a private filesystem fixture, never a router."""
import os
from pathlib import Path
import subprocess
import tempfile
import time
import unittest


@unittest.skipIf(os.name == 'nt', 'POSIX shell required')
class StartupTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='chvr-boot-test-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.base = self.root / 'jffs/cloudhelper-router'
        self.base.mkdir(parents=True)
        (self.root / 'var/run').mkdir(parents=True)
        (self.root / 'proc').mkdir()
        (self.root / 'proc/mounts').write_text('/dev/mtdblock9 /jffs jffs2 rw 0 0\n')
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        (self.bin / 'nvram').write_text('#!/bin/sh\ncase "$2" in productid) echo "${TEST_PRODUCT:-RT-AX55}";; odmpid) echo RT-AX56U_V2;; esac\n')
        (self.bin / 'nvram').chmod(0o700)
        self.service = self.base / 'service'
        self.service.write_text('#!/bin/sh\nprintf "%s\\n" "$1" >> "' + str(self.root / 'calls') + '"\nexit "${TEST_EXIT:-0}"\n')
        self.service.chmod(0o700)
        script = (Path(__file__).resolve().parents[1] / 'overlay/rom/etc/cloudhelper-jffs-start').read_text()
        # Only redirect filesystem paths; all shell control flow stays unchanged.
        script = script.replace('/dev/console', str(self.root / 'console'))
        script = script.replace('base=/jffs', 'base=' + str(self.root / 'jffs'))
        script = script.replace('runtime=/var/run', 'runtime=' + str(self.root / 'var/run'))
        script = script.replace('/proc/mounts', str(self.root / 'proc/mounts'))
        self.script = self.root / 'startup'
        self.script.write_text(script)
        self.env = dict(os.environ, PATH=str(self.bin) + ':' + os.environ['PATH'])

    def run_start(self, **extra):
        return subprocess.run(['sh', str(self.script)], env=dict(self.env, **extra), timeout=5).returncode

    def test_success_runs_once_and_creates_no_persistent_files(self):
        before = sorted(p.relative_to(self.base) for p in self.base.rglob('*'))
        self.assertEqual(self.run_start(), 0)
        self.assertIn('completed', (self.root / 'console').read_text())
        self.assertEqual(self.run_start(), 0)
        self.assertEqual((self.root / 'calls').read_text(), 'start\n')
        self.assertEqual(sorted(p.relative_to(self.base) for p in self.base.rglob('*')), before)

    def test_missing_disabled_wrong_model_or_mount_never_starts(self):
        self.service.unlink()
        self.assertEqual(self.run_start(), 0)
        self.service.write_text('#!/bin/sh\nexit 99\n')
        self.service.chmod(0o700)
        marker = self.base / 'autostart.disabled'
        marker.touch()
        self.assertEqual(self.run_start(), 0)
        marker.unlink()
        self.assertEqual(self.run_start(TEST_PRODUCT='other-model'), 0)
        (self.root / 'proc/mounts').write_text('/dev/ubi0 /jffs ubifs rw 0 0\n')
        self.assertEqual(self.run_start(), 0)
        self.assertFalse((self.root / 'calls').exists())
        self.assertFalse((self.root / 'var/run/cloudhelper-jffs-start').exists())

    def test_failure_releases_lock_and_permits_retry(self):
        self.assertEqual(self.run_start(TEST_EXIT='7'), 7)
        self.assertFalse((self.root / 'var/run/cloudhelper-jffs-start').exists())
        self.assertEqual(self.run_start(), 0)
        self.assertEqual((self.root / 'calls').read_text(), 'start\nstart\n')

    def test_concurrent_start_does_not_launch_twice(self):
        self.service.write_text('#!/bin/sh\necho start >> "' + str(self.root / 'calls') + '"\nsleep 0.3\n')
        first = subprocess.Popen(['sh', str(self.script)], env=self.env)
        try:
            deadline = time.monotonic() + 3
            while not (self.root / 'calls').exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertTrue((self.root / 'calls').exists())
            self.assertEqual(self.run_start(), 0)
            self.assertEqual(first.wait(timeout=3), 0)
            self.assertEqual((self.root / 'calls').read_text(), 'start\n')
        finally:
            if first.poll() is None:
                first.kill()
                first.wait()


@unittest.skipIf(os.name == 'nt', 'POSIX shell required')
class HttpdDeclarationTests(unittest.TestCase):
    def test_only_exact_stock_or_declared_firmware_hash_is_accepted(self):
        with tempfile.TemporaryDirectory(prefix='chvr-httpd-check-') as directory:
            root = Path(directory)
            rom = root / 'rom/etc'
            rom.mkdir(parents=True)
            (root / 'bin').mkdir()
            openssl = root / 'bin/openssl'
            openssl.write_text('#!/bin/sh\nprintf "SHA256= %s\\n" "$TEST_HASH"\n')
            openssl.chmod(0o700)
            source = (Path(__file__).resolve().parents[3] / 'router_plugin/jffs/service').read_text()
            begin = source.index('httpd_supported() {')
            end = source.index('\nhttpd_owned()', begin)
            function = source[begin:end].replace('/rom/etc/', str(rom) + '/')
            script = root / 'check'
            script.write_text('httpd=/unused\nstock_hash=' + 'a' * 64 + '\n' + function + '\nhttpd_supported\n')
            env = dict(os.environ, PATH=str(root / 'bin') + ':' + os.environ['PATH'])
            def check(actual):
                return subprocess.run(['sh', str(script)], env=dict(env, TEST_HASH=actual), timeout=3).returncode
            self.assertEqual(check('a' * 64), 0)
            self.assertNotEqual(check('b' * 64), 0)
            declaration = rom / 'cloudhelper-httpd.sha256'
            declaration.write_text('b' * 64 + '\n')
            self.assertNotEqual(check('b' * 64), 0)
            hook = rom / 'cloudhelper-jffs-start'
            hook.write_text('#!/bin/sh\n')
            hook.chmod(0o700)
            self.assertEqual(check('b' * 64), 0)
            self.assertNotEqual(check('c' * 64), 0)
            for malformed in ['b' * 63, 'b' * 65, 'B' * 64, 'b' * 64 + '\nextra', '']:
                declaration.write_text(malformed)
                self.assertNotEqual(check('b' * 64), 0)


if __name__ == '__main__':
    unittest.main()
