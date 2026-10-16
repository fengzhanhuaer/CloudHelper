#!/usr/bin/env python3
"""UART capture, POSIX command execution and terminal. Requires pyserial 3.5."""
from __future__ import annotations

import argparse
import codecs
import contextlib
import datetime as dt
import json
import os
from pathlib import Path
import queue
import re
import shlex
import subprocess
import sys
import threading
import time
import uuid


class DebugError(Exception):
    pass


class DeadlineError(DebugError):
    pass


# Passed directly to ssh as a quoted command, never via a PowerShell text pipe.
# SSH is only a temporary bootstrap; an existing console needs no SSH at all.
BOOTSTRAP = r'''set -eu
umask 077
dev=$1
seconds=$2
supervisor=$$
test -c "$dev" || { echo 'UART_NOT_CHARACTER_DEVICE'; exit 1; }
for fd in /proc/[0-9]*/fd/0; do
    target=$(readlink "$fd" 2>/dev/null || true)
    case "$target" in "$dev"|/dev/console)
        proc=${fd%/fd/0}
        name=$(cat "$proc/comm" 2>/dev/null || true)
        case "$name" in sh|ash|bash|login|*getty*)
            echo 'UART_ALREADY_HAS_READER'; exit 1 ;;
        esac ;;
    esac
done
lock=/tmp/cloudhelper-uart-debug.lock
mkdir "$lock" 2>/dev/null || { echo 'UART_DEBUG_LOCK_EXISTS'; exit 1; }
child=
watcher=
cleanup() {
    trap - EXIT HUP INT TERM
    test -z "$child" || kill -HUP "$child" 2>/dev/null || true
    test -z "$watcher" || kill "$watcher" 2>/dev/null || true
    wait 2>/dev/null || true
    rmdir "$lock" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 143' HUP INT TERM
sh -i < "$dev" > "$dev" 2>&1 &
child=$!
(
    trap - EXIT HUP INT TERM
    sleep "$seconds" &
    sleeper=$!
    trap 'kill "$sleeper" 2>/dev/null || true; wait "$sleeper" 2>/dev/null || true; exit' TERM
    wait "$sleeper"
    kill -HUP "$supervisor" 2>/dev/null || true
) &
watcher=$!
echo UART_READER_READY
# Keep SSH stdin as a lifecycle channel: STOP or EOF both clean up immediately.
read -r request || true
'''


class SessionLog:
    def __init__(self, root: Path, enabled=True, max_bytes=64 * 1024 * 1024):
        self.directory = None
        self.raw = self.events = None
        self.total = 0
        self.max_bytes = max_bytes
        if enabled:
            stamp = dt.datetime.now().strftime('%Y%m%d-%H%M%S')
            self.directory = root / (stamp + '-' + uuid.uuid4().hex[:8])
            self.directory.mkdir(parents=True, exist_ok=False)
            self.raw = (self.directory / 'rx.bin').open('xb')
            self.events = (self.directory / 'events.jsonl').open('x', encoding='utf-8')

    def event(self, kind, **fields):
        if self.events:
            self.events.write(json.dumps(dict(time=dt.datetime.now().astimezone().isoformat(),
                                              event=kind, **fields), ensure_ascii=False) + '\n')
            self.events.flush()

    def receive(self, data):
        if not data:
            return
        if self.raw:
            if self.total + len(data) > self.max_bytes:
                raise DebugError('RX log limit reached; stopped without further transmission')
            self.raw.write(data)
            self.raw.flush()
        self.event('rx', offset=self.total, bytes=len(data))
        self.total += len(data)

    def finish(self, result):
        if self.directory:
            (self.directory / 'result.json').write_text(
                json.dumps(result, indent=2, ensure_ascii=False) + '\n', encoding='utf-8')
        if self.raw:
            self.raw.close()
            self.events.close()


class UART:
    def __init__(self, port, log):
        self.port, self.log = port, log

    def receive(self):
        data = self.port.read(min(max(self.port.in_waiting, 1), 4096))
        self.log.receive(data)
        return data

    def send(self, data):
        # Do not log commands/passwords. RX can still contain device-side echo.
        count = self.port.write(data)
        self.log.event('tx', bytes=count)
        if count != len(data):
            raise DebugError('Partial serial write; command outcome unknown')

    def until(self, pattern, timeout):
        deadline, data = time.monotonic() + timeout, bytearray()
        while time.monotonic() < deadline:
            data.extend(self.receive())
            match = pattern.search(data)
            if match:
                return bytes(data), match
            if len(data) > 2 * 1024 * 1024:
                raise DebugError('Command response exceeded 2 MiB; use capture for streaming logs')
        raise DeadlineError('No completed response before timeout; command may still be running')


def marker_pattern(kind, token, status=False):
    suffix = rb':([0-9]{1,3})' if status else b''
    return re.compile(rb'(?:^|\r?\n)__CH_' + kind.encode() + b'_' + token.encode()
                      + b'__' + suffix + rb'\r?\n')


def run_command(uart, command, timeout, sync_timeout=5):
    if '\x00' in command or '\r' in command or any(ord(c) < 32 and c not in '\n\t' for c in command):
        raise DebugError('Command must be text without terminal control bytes')
    token = uuid.uuid4().hex
    # Splitting the marker format and token prevents local/TTY echo from passing.
    payload = ("printf '\\n__CH_BEGIN_%s__\\n' " + token + '; sh -c ' + shlex.quote(command)
               + "; __ch_rc=$?; printf '\\n__CH_END_%s__:%s\\n' " + token + ' "$__ch_rc"\n')
    encoded = payload.replace('\n', '\r').encode('utf-8')
    if len(encoded) > 3000:
        raise DebugError('Command wrapper exceeds 3000 bytes; split into smaller commands')
    uart.send(("printf '\\n__CH_SYNC_%s__\\n' " + token + '\r').encode('ascii'))
    try:
        uart.until(marker_pattern('SYNC', token), sync_timeout)
    except DeadlineError as exc:
        raise DeadlineError('No POSIX shell handshake; check login/CFE prompt with terminal. '
                            'Requested command was NOT sent.') from exc
    uart.send(encoded)
    data, end = uart.until(marker_pattern('END', token, status=True), timeout)
    begin = marker_pattern('BEGIN', token).search(data)
    if not begin or begin.end() > end.start():
        raise DebugError('Missing command begin marker; result is not trustworthy')
    return data[begin.end():end.start()].decode('utf-8', errors='replace').replace('\r\n', '\n'), int(end.group(1))


@contextlib.contextmanager
def bootstrap(alias, device, seconds):
    if not alias:
        yield
        return
    if alias.startswith('-') or not re.fullmatch(r'[A-Za-z0-9_.@:-]+', alias):
        raise DebugError('SSH target must be a hostname or existing SSH alias')
    if not re.fullmatch(r'/dev/[A-Za-z0-9_/.-]+', device):
        raise DebugError('Invalid remote UART device')
    remote = 'sh -c ' + shlex.quote(BOOTSTRAP) + ' uart-debug ' + shlex.quote(device) + ' ' + str(seconds)
    process = subprocess.Popen(['ssh', '-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=8',
                                '-o', 'ServerAliveInterval=5', '-o', 'ServerAliveCountMax=2',
                                alias, remote], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
    lines = queue.Queue()

    def collect():
        for line in process.stdout:
            lines.put(line.decode('utf-8', errors='replace').rstrip())
        lines.put(None)

    reader = threading.Thread(target=collect, daemon=True)
    reader.start()
    try:
        deadline = time.monotonic() + 12
        while True:
            try:
                line = lines.get(timeout=max(0.01, deadline - time.monotonic()))
            except queue.Empty as exc:
                raise DeadlineError('SSH bootstrap did not become ready') from exc
            if line == 'UART_READER_READY':
                break
            if line is None or time.monotonic() >= deadline:
                raise DebugError('SSH bootstrap failed; existing console/lock or SSH unavailable')
            print('bootstrap: ' + line, file=sys.stderr)
        yield
    finally:
        if not process.stdin.closed:
            try:
                process.stdin.write(b'STOP\n')
                process.stdin.close()
            except (BrokenPipeError, OSError):
                pass
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
        reader.join(timeout=1)
        process.stdout.close()


def display(data, decoder):
    sys.stdout.write(decoder.decode(data))
    sys.stdout.flush()


@contextlib.contextmanager
def keyboard():
    if not sys.stdin.isatty():
        raise DebugError('terminal requires an interactive console; use exec for automation')
    if os.name == 'nt':
        import msvcrt

        def read_key():
            if not msvcrt.kbhit():
                return b''
            key = msvcrt.getwch()
            if key in ('\x00', '\xe0'):
                return {'H': b'\x1b[A', 'P': b'\x1b[B', 'K': b'\x1b[D', 'M': b'\x1b[C'}.get(msvcrt.getwch(), b'')
            return key.encode('utf-8')
        yield read_key
    else:
        import select
        import termios
        import tty
        fd = sys.stdin.fileno()
        saved = termios.tcgetattr(fd)
        try:
            tty.setraw(fd)
            yield lambda: os.read(fd, 1) if select.select([fd], [], [], 0)[0] else b''
        finally:
            termios.tcsetattr(fd, termios.TCSADRAIN, saved)


def stream(uart, seconds, interactive=False):
    decoder = codecs.getincrementaldecoder('utf-8')(errors='replace')
    deadline = time.monotonic() + seconds if seconds else float('inf')
    with keyboard() if interactive else contextlib.nullcontext(lambda: b'') as read_key:
        while time.monotonic() < deadline:
            display(uart.receive(), decoder)
            key = read_key()
            if key == b'\x1d':  # Ctrl+] exits locally; Ctrl+C is forwarded.
                return
            if key:
                uart.send(key)


def positive(value):
    number = float(value)
    if not 0 < number <= 86400:
        raise argparse.ArgumentTypeError('must be >0 and <=86400')
    return number


def parser():
    root = argparse.ArgumentParser(description=__doc__)
    sub = root.add_subparsers(dest='mode', required=True)
    sub.add_parser('ports', help='list local serial ports without opening them')
    for mode in ('capture', 'exec', 'terminal'):
        p = sub.add_parser(mode)
        p.add_argument('--port', default='COM4')
        p.add_argument('--baud', type=int, default=115200)
        p.add_argument('--log-root', type=Path, default=Path.home() / 'router-debug-logs')
        p.add_argument('--no-log', action='store_true', help='disable raw RX and event files')
        if mode == 'capture':
            p.add_argument('--seconds', type=positive, default=60)
        else:
            p.add_argument('--ssh-bootstrap', metavar='ALIAS', help='explicitly start a temporary RAM shell over SSH')
            p.add_argument('--device', default='/dev/ttyAMA0')
            p.add_argument('--bootstrap-seconds', type=int, default=1800, help='remote shell maximum lifetime (1..86400)')
        if mode == 'exec':
            group = p.add_mutually_exclusive_group(required=True)
            group.add_argument('-c', '--command')
            group.add_argument('--command-file', type=Path, help='UTF-8 text; no credentials')
            p.add_argument('--timeout', type=positive, default=20)
        if mode == 'terminal':
            p.add_argument('--seconds', type=positive, default=None, help='otherwise run until Ctrl+]')
    return root


def main(argv=None):
    args = parser().parse_args(argv)
    try:
        import serial
        from serial.tools import list_ports
    except ImportError:
        print('Install dependency: python -m pip install pyserial==3.5', file=sys.stderr)
        return 125
    if args.mode == 'ports':
        print(json.dumps([dict(port=p.device, description=p.description, hwid=p.hwid)
                          for p in list_ports.comports()], ensure_ascii=False, indent=2))
        return 0
    if hasattr(args, 'bootstrap_seconds') and not 1 <= args.bootstrap_seconds <= 86400:
        print('--bootstrap-seconds must be in 1..86400', file=sys.stderr)
        return 125
    log = SessionLog(args.log_root, not args.no_log)
    result = dict(mode=args.mode, port=args.port, baud=args.baud, format='8N1', status='error')
    port = None
    code = 125
    try:
        # Configure control lines before opening. Never send BREAK or reset input.
        port = serial.Serial(port=None, baudrate=args.baud, timeout=0.1, write_timeout=3,
                             xonxoff=False, rtscts=False, dsrdtr=False)
        port.dtr = port.rts = False
        port.port = args.port
        port.open()
        uart = UART(port, log)
        log.event('open', port=args.port, baud=args.baud, mode=args.mode)
        if args.mode == 'capture':
            stream(uart, args.seconds)
            code = 0
        else:
            with bootstrap(args.ssh_bootstrap, args.device, args.bootstrap_seconds):
                if args.mode == 'exec':
                    command = args.command_file.read_text(encoding='utf-8-sig') if args.command_file else args.command
                    output, code = run_command(uart, command, args.timeout)
                    sys.stdout.write(output)
                    sys.stdout.flush()
                    result.update(remote_exit=code, output=output)
                else:
                    print('Terminal: Ctrl+] exits locally; Ctrl+C goes to router.', file=sys.stderr)
                    stream(uart, args.seconds, interactive=True)
                    code = 0
        result['status'] = 'complete'
    except KeyboardInterrupt:
        result['status'], code = 'interrupted', 130
    except (DebugError, serial.SerialException, OSError, ValueError) as exc:
        result['error'] = str(exc)
        code = 124 if isinstance(exc, DeadlineError) else 125
        print(str(exc), file=sys.stderr)
    finally:
        if port is not None:
            port.close()
        result.update(exit_code=code, rx_bytes=log.total, port_closed=True)
        log.finish(result)
        if log.directory:
            print('Session: ' + str(log.directory), file=sys.stderr)
    return code


if __name__ == '__main__':
    raise SystemExit(main())
