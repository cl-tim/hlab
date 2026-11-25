#!/usr/bin/env python3
"""Keep known plain-text Armbian ramlogs bounded. Python 3.7, root timer only.

Keep recent complete lines in the existing inode so open appenders remain valid.
Like logrotate copytruncate, a concurrent append can be lost during truncation.
Journals, compressed archives, credentials and printer logs are not edited.
"""
import os
import stat


MAX_BYTES = 1024 * 1024
KEEP_BYTES = 512 * 1024
LOG_NAMES = ('syslog', 'daemon.log', 'kern.log', 'messages', 'debug', 'user.log')


def trim_log(path):
    parent = os.path.dirname(os.path.abspath(path))
    if os.path.realpath(parent) != parent:
        raise ValueError('Symlinked ramlog directory')
    try:
        descriptor = os.open(path, os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        return False
    try:
        metadata = os.fstat(descriptor)
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink != 1:
            raise ValueError('Ramlog is not a singly linked regular file')
        if metadata.st_size <= MAX_BYTES:
            return False
        os.lseek(descriptor, -KEEP_BYTES, os.SEEK_END)
        data = os.read(descriptor, KEEP_BYTES)
        newline = data.find(b'\n')
        data = data[newline + 1:] if newline >= 0 else b''
        os.lseek(descriptor, 0, os.SEEK_SET)
        remaining = memoryview(data)
        while remaining:
            count = os.write(descriptor, remaining)
            if count <= 0:
                raise OSError('Short ramlog write')
            remaining = remaining[count:]
        os.ftruncate(descriptor, len(data))
        return True
    finally:
        os.close(descriptor)


def maintain(roots=('/var/log', '/var/log.hdd')):
    for root in roots:
        for name in LOG_NAMES:
            for suffix in ('', '.1'):
                trim_log(os.path.join(root, name + suffix))


if __name__ == '__main__':
    maintain()
