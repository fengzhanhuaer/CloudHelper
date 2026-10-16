"""Protocol failure tests; POSIX also exercises a real shell without a router."""
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

import router_serial as rs


class ScriptedPort:
    def __init__(self, response='success'):
        self.response, self.pending, self.writes = response, bytearray(), []

    @property
    def in_waiting(self):
        return min(len(self.pending), 7)  # Fragment every marker across reads.

    def read(self, count):
        data = bytes(self.pending[:count])
        del self.pending[:count]
        return data

    def write(self, data):
        self.writes.append(data)
        self.pending.extend(data.replace(b'\r', b'\r\n'))  # Device-side echo.
        token = re.search(rb'\b[0-9a-f]{32}\b', data).group()
        if len(self.writes) == 1:
            if self.response != 'echo_only':
                self.pending.extend(b'\r\n__CH_SYNC_' + token + b'__\r\n')
        elif self.response != 'timeout':
            if self.response != 'missing_begin':
                self.pending.extend(b'\r\n__CH_BEGIN_' + token + b'__\r\n')
            self.pending.extend(b'Linux test board\r\n\r\n__CH_END_' + token + b'__:7\r\n')
        return len(data)


class SerialTests(unittest.TestCase):
    def uart(self, port):
        return rs.UART(port, rs.SessionLog(Path('.'), enabled=False))

    def test_fragmented_reply_and_nonzero_exit(self):
        output, code = rs.run_command(self.uart(ScriptedPort()), 'uname -a; exit 7', 0.1)
        self.assertEqual(output, 'Linux test board\n')
        self.assertEqual(code, 7)

    def test_echo_cannot_authorize_real_command(self):
        port = ScriptedPort('echo_only')
        with self.assertRaisesRegex(rs.DeadlineError, 'NOT sent'):
            rs.run_command(self.uart(port), 'must_not_execute', 0.02, sync_timeout=0.02)
        self.assertEqual(len(port.writes), 1)
        self.assertNotIn(b'must_not_execute', port.writes[0])

    def test_execution_timeout_does_not_retry(self):
        port = ScriptedPort('timeout')
        with self.assertRaises(rs.DeadlineError):
            rs.run_command(self.uart(port), 'slow_query', 0.02)
        self.assertEqual(len(port.writes), 2)

    def test_end_without_begin_is_rejected(self):
        with self.assertRaisesRegex(rs.DebugError, 'begin marker'):
            rs.run_command(self.uart(ScriptedPort('missing_begin')), 'query', 0.1)

    def test_invalid_or_oversize_input_sends_nothing(self):
        for command in ('x\x03', 'x' * 4000, 'x\rreboot'):
            port = ScriptedPort()
            with self.assertRaises(rs.DebugError):
                rs.run_command(self.uart(port), command, 0.1)
            self.assertEqual(port.writes, [])

    def test_capture_never_transmits(self):
        port = ScriptedPort()
        rs.stream(self.uart(port), 0.01)
        self.assertEqual(port.writes, [])

    def test_raw_log_preserves_invalid_utf8_and_bounds_disk_use(self):
        with tempfile.TemporaryDirectory() as folder:
            log = rs.SessionLog(Path(folder), max_bytes=3)
            log.receive(b'\x00\xffA')
            with self.assertRaises(rs.DebugError):
                log.receive(b'B')
            log.finish({'status': 'error'})
            self.assertEqual((log.directory / 'rx.bin').read_bytes(), b'\x00\xffA')

    @unittest.skipIf(os.name == 'nt', 'real /bin/sh test runs in local WSL')
    def test_real_posix_shell_quoting_and_exit(self):
        import select
        process = subprocess.Popen(['/bin/sh'], stdin=subprocess.PIPE,
                                   stdout=subprocess.PIPE, stderr=subprocess.STDOUT)

        class PipePort:
            in_waiting = 4096

            def read(self, count):
                return os.read(process.stdout.fileno(), count) if select.select([process.stdout], [], [], 0.02)[0] else b''

            def write(self, data):
                process.stdin.write(data.replace(b'\r', b'\n'))
                process.stdin.flush()
                return len(data)

        try:
            uart = self.uart(PipePort())
            output, code = rs.run_command(uart, 'printf \'a "quote" and %s\\n\' "中 文"\nexit 9', 2)
            self.assertEqual((output, code), ('a "quote" and 中 文\n', 9))
            self.assertEqual(rs.run_command(uart, 'printf next', 2), ('next', 0))
        finally:
            process.stdin.close()
            process.wait(timeout=2)
            process.stdout.close()


if __name__ == '__main__':
    unittest.main()
