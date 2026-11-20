package migrate

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
)

// verifiedQIDIFiles accepts only the catalogue's exact bytes, regardless of
// whether they arrived from a vendor download or removable/offline media.
func verifiedQIDIFiles(directory string, release QIDIRelease, hostOnly bool) (map[string][]byte, error) {
	directory, err := localPath(directory)
	if err != nil {
		return nil, err
	}
	artifacts := map[string]QIDIArtifact{"host.deb": release.Host}
	if !hostOnly {
		artifacts["screen.tft"] = release.Screen
	}
	files := map[string][]byte{}
	for name, artifact := range artifacts {
		address, err := url.Parse(artifact.URL)
		if err != nil {
			return nil, err
		}
		filename := filepath.Join(directory, path.Base(address.Path))
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Bytes {
			return nil, fmt.Errorf("missing or wrong-sized reviewed QIDI payload: %s", filename)
		}
		raw, err := os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) != artifact.Bytes || hashBytes(raw) != artifact.SHA256 {
			return nil, fmt.Errorf("QIDI payload checksum mismatch: %s", filename)
		}
		files[name] = raw
	}
	return files, nil
}

func (b *nativeBackend) stageQIDIFiles(files map[string][]byte, release QIDIRelease) (string, func(), error) {
	directory := "/var/lib/hlab/qidi-payloads/" + randomToken() + randomToken()
	expected := map[string]QIDIArtifact{"host.deb": release.Host}
	if _, ok := files["screen.tft"]; ok {
		expected["screen.tft"] = release.Screen
	}
	encoded, _ := json.Marshal(expected)
	literal, _ := json.Marshal(string(encoded))
	prefix := "directory=" + quotePython(directory) + "\n"
	script := prefix + "expected=json.loads(" + string(literal) + ")\n" + qidiStageScript
	payload, err := tarFiles(files)
	if err != nil {
		return "", nil, err
	}
	raw, _, err := b.sshCapture("sudo -n python3 -I -c "+quote("import json\n"+script), payload)
	if err != nil {
		return "", nil, fmt.Errorf("stage workstation firmware: %w", err)
	}
	if string(raw) != "staged\n" {
		return "", nil, fmt.Errorf("printer did not confirm verified firmware staging")
	}
	cleanup := func() {
		_, _, err := b.sshCapture("sudo -n python3 -I -c "+quote(prefix+qidiStageCleanup), nil)
		if err != nil {
			fmt.Fprintf(b.errOut, "Firmware staging retained at %s: %v\n", directory, err)
		}
	}
	return directory, cleanup, nil
}

func quotePython(value string) string { raw, _ := json.Marshal(value); return string(raw) }

const qidiStageScript = `import hashlib, os, pathlib, shutil, sys, tarfile
os.umask(0o077)
base=pathlib.Path('/var/lib/hlab/qidi-payloads')
base.mkdir(parents=True, mode=0o700, exist_ok=True)
if os.geteuid()!=0 or base.resolve()!=base or base.stat().st_uid!=0 or base.stat().st_mode & 0o077:
    raise RuntimeError('firmware staging root is not private and root owned')
target=pathlib.Path(directory)
if target.parent!=base or len(target.name)!=32 or any(c not in '0123456789abcdef' for c in target.name):
    raise RuntimeError('invalid staging directory')
target.mkdir(mode=0o700)
try:
    seen=set()
    with tarfile.open(fileobj=sys.stdin.buffer, mode='r|') as archive:
        for member in archive:
            if member.name not in expected or member.name in seen or not member.isfile() or member.size!=expected[member.name]['bytes']:
                raise RuntimeError('unexpected firmware archive member')
            seen.add(member.name)
            digest=hashlib.sha256()
            count=0
            with archive.extractfile(member) as source, (target/member.name).open('xb') as output:
                for chunk in iter(lambda: source.read(1024*1024), b''):
                    count+=len(chunk)
                    if count>member.size: raise RuntimeError('oversized firmware')
                    output.write(chunk)
                    digest.update(chunk)
                output.flush()
                os.fsync(output.fileno())
            if count!=member.size or digest.hexdigest()!=expected[member.name]['sha256']:
                raise RuntimeError('firmware staging checksum mismatch')
    if seen!=set(expected): raise RuntimeError('missing firmware payload')
except BaseException:
    shutil.rmtree(str(target))
    raise
print('staged')
`

const qidiStageCleanup = `import os, pathlib, re, shutil
target=pathlib.Path(directory)
base=pathlib.Path('/var/lib/hlab/qidi-payloads')
if target.parent!=base or not re.match(r'^[0-9a-f]{32}$',target.name) or target.resolve()!=target or target.stat().st_uid!=0:
    raise RuntimeError('invalid staging cleanup target')
shutil.rmtree(str(target))
`
