#!/usr/bin/env python3
"""Bounded USB evidence for hlab. Python 3.7; no serial numbers or printer IO.

udev only sends a nonblocking datagram. A single socket-activated writer owns
rotation and fsync, keeping storage latency out of the udev event handler.
"""
import json
import math
import os
from pathlib import Path
import re
import socket
import stat
import sys
import time

SOCKET = '/run/hlab-usb-events.sock'
DIRECTORY = Path('/var/lib/hlab-usb-events')
MAX_BYTES = 256 * 1024
MAX_EVENT_BYTES = 1024
BOOT = re.compile(r'^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$')
PORT = re.compile(r'^[0-9]{1,3}-[0-9]{1,3}(?:\.[0-9]{1,3}){0,7}$')
IDS = re.compile(r'^[0-9a-fA-F]{1,4}/[0-9a-fA-F]{1,4}/[0-9a-fA-F]{1,4}$')


def event(action, port, ids, boot_id=None):
    record = dict(schema=1, action=action, port=port, ids=ids.lower(),
                  bootId=boot_id or Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
                  epoch=time.time(), monotonic=time.monotonic())
    validate(record)
    return record


def validate(record):
    if not isinstance(record, dict) or set(record) != {'schema', 'action', 'port', 'ids', 'bootId', 'epoch', 'monotonic'}:
        raise ValueError('Invalid USB event fields')
    if type(record['schema']) is not int or record['schema'] != 1 or record['action'] not in ('add', 'remove', 'start'):
        raise ValueError('Invalid USB event schema/action')
    if not isinstance(record['bootId'], str) or not BOOT.fullmatch(record['bootId']):
        raise ValueError('Invalid boot identity')
    if not isinstance(record['port'], str) or not isinstance(record['ids'], str):
        raise ValueError('Invalid USB identity')
    if record['action'] == 'start':
        if record['port'] != '-' or record['ids'] != '-':
            raise ValueError('Invalid start event')
    elif not PORT.fullmatch(record['port']) or not IDS.fullmatch(record['ids']):
        raise ValueError('Invalid USB port/product IDs')
    for field in ('epoch', 'monotonic'):
        value = record[field]
        if type(value) not in (int, float) or not math.isfinite(value) or value < 0:
            raise ValueError('Invalid event clock')


def emit(action, port, ids, endpoint=SOCKET):
    data = json.dumps(event(action, port, ids), separators=(',', ':')).encode('ascii')
    with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as client:
        client.setblocking(False)
        client.sendto(data, endpoint)


def trusted_directory(directory):
    # Keep root's append/rotation targets out of printer-user-writable trees.
    for path in [directory] + list(directory.parents):
        info = path.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError('Untrusted USB log directory')


def append_record(record, directory=DIRECTORY):
    validate(record)
    trusted_directory(directory)
    path = directory / 'events.jsonl'
    previous = directory / 'events.jsonl.1'
    data = (json.dumps(record, separators=(',', ':'), sort_keys=True) + '\n').encode('ascii')
    if len(data) > MAX_EVENT_BYTES:
        raise ValueError('Oversized USB event')
    for target in (path, previous):
        if target.is_symlink():
            raise ValueError('Symlinked USB log')
        if target.exists():
            info = target.lstat()
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o022:
                raise ValueError('Untrusted USB log')
    if path.exists() and path.stat().st_size + len(data) > MAX_BYTES:
        os.replace(str(path), str(previous))
    fd = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_APPEND | os.O_NOFOLLOW, 0o644)
    try:
        os.fchmod(fd, 0o644)
        with os.fdopen(fd, 'ab', closefd=False) as stream:
            stream.write(data)
            stream.flush()
            os.fsync(fd)
    finally:
        os.close(fd)
    # Persist creation/rotation as well as the record itself.
    directory_fd = os.open(str(directory), os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)


def serve():
    if os.geteuid() != 0:
        raise ValueError('USB evidence writer requires root')
    if os.environ.get('LISTEN_PID') != str(os.getpid()) or os.environ.get('LISTEN_FDS') != '1':
        raise ValueError('Expected one systemd datagram socket')
    with socket.socket(fileno=3) as receiver:
        if receiver.family != socket.AF_UNIX or receiver.getsockopt(socket.SOL_SOCKET, socket.SO_TYPE) != socket.SOCK_DGRAM:
            raise ValueError('Expected a Unix datagram socket')
        append_record(event('start', '-', '-'))
        # Type=notify makes installation wait until the first record is durable.
        address = os.environ['NOTIFY_SOCKET']
        if address.startswith('@'):
            address = '\0' + address[1:]
        with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as notifier:
            notifier.sendto(b'READY=1', address)
        while True:
            data = receiver.recv(MAX_EVENT_BYTES + 1)
            try:
                if len(data) > MAX_EVENT_BYTES:
                    raise ValueError('Oversized USB event')
                record = json.loads(data.decode('ascii'))
                append_record(record)
            except (ValueError, UnicodeError):
                print('hlab USB logger: discarded invalid event', file=sys.stderr)


def main(args):
    if args == ['serve']:
        serve()
    elif len(args) == 4 and args[0] == 'emit' and args[1] in ('add', 'remove'):
        emit(*args[1:])
    else:
        raise ValueError('Expected serve or emit add|remove PORT VENDOR/PRODUCT/VERSION')


if __name__ == '__main__':
    main(sys.argv[1:])
