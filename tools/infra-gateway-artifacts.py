"""Root-private, hash-guarded Guest binaries for the opt-in VM160 lab only."""
import base64
import gzip
import hashlib
import json
import os
import pathlib
import re
import stat
import subprocess
import sys

assert not sys.flags.optimize and os.geteuid() == 0
operation, marker = sys.argv[1:]
assert re.fullmatch(r"svc[0-9a-f]{24}", marker)
base = pathlib.Path('/run/vc-workspace-infra-' + marker)
root = base / 'artifacts'
targets = {
    'vc-workspace-guest-agent': pathlib.Path('/usr/local/sbin/vc-workspace-guest-agent'),
    'pam_vcworkspace.so': pathlib.Path('/usr/local/lib/security/pam_vcworkspace.so'),
    'pam_vcworkspace_native.so': pathlib.Path('/usr/local/lib/security/pam_vcworkspace_native.so'),
    'xrdp': pathlib.Path('/usr/sbin/xrdp'),
}
required_targets = set(targets) - {'xrdp'}

def select_targets(names):
    assert set(names) in (required_targets, set(targets))
    return {name: targets[name] for name in names}

def xrdp_service(operation):
    subprocess.run(['systemctl', operation, 'xrdp.service'], check=True, timeout=30,
                   stdout=subprocess.DEVNULL)

def xrdp_idle():
    assert subprocess.run(['pgrep', '-x', 'xrdp'], capture_output=True).returncode == 1

def directory(path):
    for p in (path, *path.parents):
        m = p.lstat()
        assert stat.S_ISDIR(m.st_mode) and m.st_uid == 0 and not m.st_mode & 0o022

def contents(path):
    directory(path.parent)
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), 'rb') as stream:
        m = os.fstat(stream.fileno())
        assert stat.S_ISREG(m.st_mode) and m.st_uid == 0 and m.st_nlink == 1 and not m.st_mode & 0o022 and m.st_size < 20000000
        return stream.read(), stat.S_IMODE(m.st_mode), m.st_gid

def digest(raw): return hashlib.sha256(raw).hexdigest()

def create(path, raw, mode=0o600):
    directory(path.parent)
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode), 'wb') as stream:
        stream.write(raw); stream.flush(); os.fsync(stream.fileno())
        os.fchmod(stream.fileno(), mode)

def idle():
    for process in ('Xorg', 'xrdp-sesexec', 'vc-workspace-guest-agent'):
        assert subprocess.run(['pgrep', '-f' if process == 'vc-workspace-guest-agent' else '-x',
                               '^/usr/local/sbin/vc-workspace-guest-agent' if process == 'vc-workspace-guest-agent' else process], capture_output=True).returncode == 1
    pam = pathlib.Path('/etc/pam.d/xrdp-sesman').read_bytes()
    assert b'pam_vcworkspace' not in pam

directory(base)
if operation == 'begin':
    idle()
    assert not os.path.lexists(root)
    request = json.load(sys.stdin)
    targets = select_targets(request)
    if 'xrdp' in targets:
        assert subprocess.check_output(['dpkg-query', '-W', '-f=${Version}', 'xrdp'], text=True) == '0.10.1-3.1+deb13u2'
        assert digest(contents(targets['xrdp'])[0]) == 'c47ea4813ba8d5da33f589760766d594ba800fab17162ea7ee2b1bdcad7a00e5'
        xrdp_service('is-active')
    for value in request.values(): assert re.fullmatch(r'[a-f0-9]{64}', value)
    for target in targets.values(): directory(target.parent)
    root.mkdir(mode=0o700)
    manifest = {}
    for name, target in targets.items():
        original = None
        if os.path.lexists(target):
            raw, mode, gid = contents(target)
            create(root / (name + '.before'), raw)
            original = dict(sha256=digest(raw), mode=mode, gid=gid)
        manifest[name] = dict(before=original, after=request[name])
    create(root / 'manifest.json', json.dumps(manifest).encode())
    print('binary baseline saved; installed files unchanged')
else:
    manifest = json.loads(contents(root / 'manifest.json')[0])
    targets = select_targets(manifest)
    if operation == 'verify-empty':
        idle()
        assert json.load(sys.stdin) == {name: value['after'] for name, value in manifest.items()}
        for name, target in targets.items():
            assert not os.path.lexists(root / (name + '.gz'))
            before = manifest[name]['before']
            if before: assert digest(contents(target)[0]) == before['sha256']
            else: assert not os.path.lexists(target)
        print('prior rejected upload wrote no chunks; binary baseline unchanged')
    elif operation == 'chunk':
        request = json.load(sys.stdin)
        assert set(request) == {'name', 'offset', 'data'} and request['name'] in targets
        raw = base64.b64decode(request['data'], validate=True)
        assert 0 < len(raw) <= 131072 and type(request['offset']) is int and 0 <= request['offset'] < 20000000
        destination = root / (request['name'] + '.gz')
        fd = os.open(destination, os.O_WRONLY | os.O_NOFOLLOW | os.O_NONBLOCK | (os.O_CREAT | os.O_EXCL if request['offset'] == 0 else 0), 0o600)
        with os.fdopen(fd, 'r+b') as stream:
            m = os.fstat(stream.fileno())
            assert stat.S_ISREG(m.st_mode) and m.st_uid == 0 and m.st_nlink == 1 and stat.S_IMODE(m.st_mode) == 0o600 and m.st_size == request['offset']
            stream.seek(request['offset']); stream.write(raw); stream.flush(); os.fsync(stream.fileno())
        print('chunk persisted')
    elif operation == 'install':
        idle()
        payload = {}
        for name, target in targets.items():
            with gzip.GzipFile(fileobj=__import__('io').BytesIO(contents(root / (name + '.gz'))[0])) as stream:
                raw = stream.read(20000001)
            assert len(raw) < 20000000 and digest(raw) == manifest[name]['after']
            assert raw[:6] == b'\x7fELF\x02\x01' and raw[18:20] == b'\x3e\x00'
            before = manifest[name]['before']
            if before is None: assert not os.path.lexists(target)
            else: assert digest(contents(target)[0]) == before['sha256']
            payload[name] = raw
        if 'xrdp' in targets:
            xrdp_service('stop')
            xrdp_idle()
        for name, target in targets.items():
            temporary = target.with_name(target.name + '.' + marker)
            create(temporary, payload[name], 0o755)
            os.replace(temporary, target)
        if 'xrdp' in targets:
            subprocess.run(['/usr/sbin/xrdp', '--version'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
            xrdp_service('start')
            xrdp_service('is-active')
        print('verified amd64 lab binaries installed')
    elif operation == 'restore':
        idle()
        # Validate every target and backup before stopping the idle listener.
        for name, target in targets.items():
            before = manifest[name]['before']
            if os.path.lexists(target):
                assert digest(contents(target)[0]) in (manifest[name]['after'], before['sha256'] if before else None)
            else: assert before is None
            if before: assert digest(contents(root / (name + '.before'))[0]) == before['sha256']
        if 'xrdp' in targets:
            xrdp_service('stop')
            xrdp_idle()
        for name, target in targets.items():
            before = manifest[name]['before']
            if os.path.lexists(target):
                assert digest(contents(target)[0]) in (manifest[name]['after'], before['sha256'] if before else None)
            else: assert before is None
            if before:
                raw = contents(root / (name + '.before'))[0]
                assert digest(raw) == before['sha256']
                temporary = target.with_name(target.name + '.' + marker)
                create(temporary, raw, before['mode']); os.chown(temporary, 0, before['gid'])
                os.replace(temporary, target)
                assert contents(target) == (raw, before['mode'], before['gid'])
            elif os.path.lexists(target): target.unlink()
        if 'xrdp' in targets:
            xrdp_service('start')
            xrdp_service('is-active')
        for name in targets:
            for suffix in ('.before', '.gz'):
                p = root / (name + suffix)
                if os.path.lexists(p): contents(p); p.unlink()
        (root / 'manifest.json').unlink(); root.rmdir()
        print('original Guest binaries restored and verified')
    else: raise AssertionError('unknown artifact operation')
