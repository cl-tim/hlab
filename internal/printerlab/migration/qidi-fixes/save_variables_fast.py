"""Durable, no-op-aware stock Plus4 saves. Python 3.7, GPL-3.0-only.

Inspired by Valve-Oil c89dddc's save_variables_fast.py. This implementation
uses QIDI's ConfigParser/literal semantics, but flushes both file and directory.
It does not migrate names or change saved values during installation.
"""
import ast
import configparser
import hashlib
import io
import logging
import os
import re
import stat
import tempfile

STOCK_SAVE_SHA256 = '0327ce54b1a7286d97b0b2fbe008ecc9a36057a5d12642fd6237552c5e77b682'
SAVE_PROFILES = {
    STOCK_SAVE_SHA256: 'box-era',
    '168b64a2ae3022b7a3a6dac24fb9c0de541b152bd0cf1bbbc385b72c14cc8edf': 'legacy-1.6.0',
    '362397ede2e1d79819b090632ab9bf11e8df80ef1b853a65f6c5a339afd057bf': 'legacy-1.6.0',
}
STOCK_BOX_SHA256 = {
    '1.8.2': 'fdeb7df7366e7712d9a74e46267f28781c44680f9c00da954b051fa7cb652d4c',
    '2.1.1': 'c563d1352623774ca92fd2c500ef80bcb8f0407381981fa811c7476234fcaac7',
}
NAME = re.compile(r'[A-Za-z0-9_]+\Z')


def check_native_shadow(extras):
    if any(name.startswith('save_variables.') and name.endswith(('.so', '.pyd'))
           for name in os.listdir(str(extras))):
        raise ValueError('Unreviewed native save_variables module shadows the Python source')


def validate_sources(extras, version):
    profile = 'legacy-1.6.0' if version == '1.6.0' else 'box-era'
    if version not in ('1.6.0', '1.8.2', '2.1.1'):
        raise ValueError('Unreviewed saved-variable firmware version')
    check_native_shadow(extras)
    with open(os.path.join(str(extras), 'save_variables.py'), 'rb') as stream:
        if SAVE_PROFILES.get(hashlib.sha256(stream.read()).hexdigest()) != profile:
            raise ValueError('Unreviewed saved-variable source: save_variables.py')
    if version == '1.6.0':
        if any(name.startswith('box_extras.') for name in os.listdir(str(extras))):
            raise ValueError('Unreviewed Box additions on firmware 1.6.0')
        return
    for filename, expected in (('box_extras.so', STOCK_BOX_SHA256[version]),):
        path = os.path.join(str(extras), filename)
        if filename == 'box_extras.so' and not os.path.exists(path):
            continue
        with open(path, 'rb') as stream:
            if hashlib.sha256(stream.read()).hexdigest() != expected:
                raise ValueError('Unreviewed saved-variable source: ' + filename)


def durable_replace(filename, content):
    # Resolve the configured symlink just as opening QIDI's file does. Never
    # replace the link itself. A unique sibling avoids predictable-temp races.
    path = os.path.realpath(filename)
    metadata = os.stat(path)
    fd, temporary = tempfile.mkstemp(prefix='.hlab-variables-', dir=os.path.dirname(path))
    try:
        with os.fdopen(fd, 'w') as stream:
            stream.write(content)
            stream.flush()
            current = os.fstat(stream.fileno())
            if (current.st_uid, current.st_gid) != (metadata.st_uid, metadata.st_gid):
                os.fchown(stream.fileno(), metadata.st_uid, metadata.st_gid)
            os.fchmod(stream.fileno(), stat.S_IMODE(metadata.st_mode))
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory = os.open(os.path.dirname(path), os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


class SaveVariablesFast:
    def __init__(self, config):
        self.printer = config.get_printer()
        self.gcode = self.printer.lookup_object('gcode')
        self.save_vars = self.printer.load_object(config, 'save_variables')
        self.original = self.save_vars.cmd_SAVE_VARIABLE
        self.writes = self.skipped = self.fallbacks = self.errors = 0
        self.active = False
        self.box_hook = 'absent'
        self.vendor = None
        self.profile = None
        self.printer.register_event_handler('klippy:connect', self.connect)

    def connect(self):
        # Fail startup instead of claiming to optimize a different implementation.
        extras = os.path.dirname(__file__)
        try:
            check_native_shadow(extras)
        except ValueError as exc:
            raise self.printer.config_error(str(exc))
        with open(os.path.join(extras, 'save_variables.py'), 'rb') as stream:
            self.profile = SAVE_PROFILES.get(hashlib.sha256(stream.read()).hexdigest())
            if self.profile is None:
                raise self.printer.config_error('Unreviewed save_variables implementation')
        binary = os.path.join(extras, 'box_extras.so')
        if self.profile == 'legacy-1.6.0' and any(name.startswith('box_extras.') for name in os.listdir(extras)):
            raise self.printer.config_error('Unreviewed Box additions on firmware 1.6.0')
        if os.path.exists(binary):
            with open(binary, 'rb') as stream:
                if self.profile == 'legacy-1.6.0' or hashlib.sha256(stream.read()).hexdigest() not in STOCK_BOX_SHA256.values():
                    raise self.printer.config_error('Unreviewed Box saved-variable implementation')
        owner = self.printer.lookup_object('box_extras', None)
        if owner is not None:
            if self.profile == 'legacy-1.6.0':
                raise self.printer.config_error('Box hooks are not reviewed for firmware 1.6.0')
            if getattr(owner, 'save_variables', None) is not self.save_vars:
                raise self.printer.config_error('Unrecognized Box variable store')
            original = owner.save_variable
            self.vendor = (owner, original)
            owner.save_variable = self.save_box_variable
            self.box_hook = 'active'
        self.gcode.register_command('SAVE_VARIABLE', None)
        self.gcode.register_command('SAVE_VARIABLE', self.cmd_SAVE_VARIABLE,
                                    desc=self.save_vars.cmd_SAVE_VARIABLE_help)
        self.save_vars.cmd_SAVE_VARIABLE = self.cmd_SAVE_VARIABLE
        self.active = True

    def save(self, name, value, error):
        """Return False only before IO when stock semantics need the stock path."""
        try:
            if type(name) is not str or not NAME.fullmatch(name):
                return False
            values = dict(self.save_vars.allVariables)
            values[name] = value
            parser = configparser.ConfigParser()
            parser.add_section('Variables')
            for key, item in sorted(values.items()):
                if type(key) is not str or not NAME.fullmatch(key) or '%' in repr(item):
                    return False
                parser.set('Variables', key, repr(item))
            # Model QIDI's reload in memory, including case collisions, ordering,
            # fresh nested objects, default attributes and literal parse errors.
            saved = {key: ast.literal_eval(text) for key, text in parser.items('Variables')}
            unchanged = repr(saved) == repr(self.save_vars.allVariables)
            if unchanged:
                for key, default in (self.save_vars.default_values if self.profile == 'box-era' else {}).items():
                    setattr(self.save_vars, key, saved.get(key, default))
                self.skipped += 1
                return True
            output = io.StringIO()
            parser.write(output)
        except (ValueError, TypeError, SyntaxError, configparser.Error, RecursionError):
            return False
        try:
            durable_replace(self.save_vars.filename, output.getvalue())
        except Exception:
            self.errors += 1
            # A directory fsync can fail after replacement. Reconcile memory
            # with the readable file, report failure, and never retry in-place.
            self.save_vars.loadVariables()
            logging.exception('Unable to durably save variable')
            raise error('Unable to durably save variable')
        self.save_vars.allVariables = saved
        for key, default in (self.save_vars.default_values if self.profile == 'box-era' else {}).items():
            setattr(self.save_vars, key, saved.get(key, default))
        self.writes += 1
        return True

    def cmd_SAVE_VARIABLE(self, gcmd):
        try:
            value = ast.literal_eval(gcmd.get('VALUE'))
        except (ValueError, SyntaxError):
            self.fallbacks += 1
            return self.original(gcmd)
        if not self.save(gcmd.get('VARIABLE'), value, gcmd.error):
            self.fallbacks += 1
            return self.original(gcmd)

    def save_box_variable(self, varname, value):
        owner, original = self.vendor
        if getattr(owner, 'save_variables', None) is not self.save_vars or not self.save(varname, value, self.gcode.error):
            self.fallbacks += 1
            return original(varname, value)

    def get_status(self, eventtime):
        return dict(active=self.active, box_hook=self.box_hook, profile=self.profile, writes=self.writes,
                    skipped=self.skipped, fallbacks=self.fallbacks, errors=self.errors)


def load_config(config):
    return SaveVariablesFast(config)
