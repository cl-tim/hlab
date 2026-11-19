// Package backup copies the state hlab keeps on a machine back to where hlab
// runs, and puts it back again.
//
// What it protects is small and irreplaceable: a DNS zone, a DHCP lease
// database, service credentials, printer configuration. What it deliberately
// does not protect is bulk data mounted from elsewhere on the machine — a
// media library is large, is not hlab's, and would turn a cheap safety net
// into something nobody runs.
package backup

import (
	"fmt"
	"strings"

	"github.com/cl-tim/hlab/internal/shellword"
)

// RemoteDir is the directory backed up on each machine.
const RemoteDir = "/opt/hlab/stacks"

// Markers delimiting the archive in the script's output.
const (
	MarkerBegin    = "##HLAB#BACKUP#"
	MarkerEnd      = "##HLAB#ENDBACKUP#"
	MarkerNothing  = "hlab:nothing-to-back-up"
	MarkerRestored = "hlab:restored"
)

// Excluded holds the patterns left out of an archive.
//
// The point of this backup is rebuilding a machine, not preserving its
// history. Everything here is either regenerated on first run, or is live
// state that should not be restored at all:
//
//   - Logs and caches come back on their own and dominate the archive —
//     unfiltered, a fleet of a few megabytes of configuration was carried as
//     fourteen megabytes of DNS blocklists and printer agent logs, plus several
//     hundred downloaded UI icons.
//   - A database copied while its service is running is the dangerous one. The
//     file plus its write-ahead log is an inconsistent snapshot, and restoring
//     it can produce a corrupt database rather than an old one. Home
//     Assistant's recorder holds history, which is data rather than
//     configuration, and is excluded for both reasons.
var Excluded = []string{
	"*.log", "*.log.*", "logs",
	"blocklists", "stats", "cache.bin", ".cache", "deps", "tts",
	"*.db", "*.db-wal", "*.db-shm",
	"*.sqlite", "*.sqlite-wal", "*.sqlite-shm",
}

// excludeArgs renders the exclusions as tar arguments.
func excludeArgs() string {
	var b strings.Builder
	for _, pattern := range Excluded {
		b.WriteString(" --exclude=")
		b.WriteString(shellword.Quote(pattern))
	}
	return b.String()
}

// archiveTemplate tars the stack directory and prints it as base64 between two
// markers, with a checksum and a length.
//
// Base64 rather than raw bytes: the archive shares a stream with whatever else
// the shell might emit, and a stray line of output in the middle of a binary
// tar is a corrupt archive that only announces itself when someone tries to
// restore from it. The checksum turns that from a silent problem into a loud
// one at the moment of taking the backup.
const archiveTemplate = `
set -eu

SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

if [ ! -d __DIR__ ]; then
  echo '__M_NOTHING__'
  exit 0
fi

TMP=$(mktemp /tmp/hlab-backup-XXXXXX.tgz)
trap 'rm -f "$TMP"' EXIT

# Ownership and modes are preserved, because a secret restored world-readable
# is not the thing that was backed up.
$SUDO tar -czf "$TMP"__EXCLUDE__ -C __PARENT__ __BASE__ 2>/dev/null

SUM=$($SUDO sha256sum "$TMP" | cut -d' ' -f1)
SIZE=$($SUDO wc -c < "$TMP" | tr -d ' ')
FILES=$($SUDO tar -tzf "$TMP" 2>/dev/null | wc -l | tr -d ' ')

printf '__M_BEGIN__%s#%s#%s#\n' "$SUM" "$SIZE" "$FILES"
$SUDO base64 "$TMP"
printf '__M_END__\n'
`

// BuildArchiveScript renders the script that produces a backup.
func BuildArchiveScript() string {
	parent, base := split(RemoteDir)
	return strings.NewReplacer(
		"__DIR__", shellword.Quote(RemoteDir),
		"__PARENT__", shellword.Quote(parent),
		"__BASE__", shellword.Quote(base),
		"__EXCLUDE__", excludeArgs(),
		"__M_BEGIN__", MarkerBegin,
		"__M_END__", MarkerEnd,
		"__M_NOTHING__", MarkerNothing,
	).Replace(archiveTemplate)
}

// StageCommand is the command that receives a base64 archive on stdin.
//
// The data travels as the command's input rather than inside the script,
// because a script carrying twenty megabytes of payload is awkward to quote
// and impossible to read when something goes wrong.
const StageCommand = "sh -c 'cat > /tmp/hlab-restore.b64'"

// restoreTemplate verifies a staged archive and puts it back.
//
// The order is the safety property. The current state is copied aside before
// anything is overwritten, so a restore from a bad archive is itself
// reversible; the stacks are stopped before their data changes underneath
// them; and the archive's checksum is verified before any of that begins.
const restoreTemplate = `
set -eu

SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

STAGED=/tmp/hlab-restore.b64
ARCHIVE=/tmp/hlab-restore.tgz
trap 'rm -f "$STAGED" "$ARCHIVE"' EXIT

base64 -d < "$STAGED" > "$ARCHIVE"

SUM=$(sha256sum "$ARCHIVE" | cut -d' ' -f1)
if [ "$SUM" != __SUM__ ]; then
  echo 'hlab:error the archive did not survive the transfer; nothing was changed' >&2
  exit 50
fi
if ! tar -tzf "$ARCHIVE" >/dev/null 2>&1; then
  echo 'hlab:error the archive is not readable; nothing was changed' >&2
  exit 51
fi

# Stop what is running before its data moves under it.
if command -v docker >/dev/null 2>&1; then
  for d in __DIR__/*/; do
    [ -f "$d/compose.yaml" ] || continue
    ( cd "$d" && $SUDO docker compose down >/dev/null 2>&1 ) || true
  done
fi

# Keep what is there now. A restore from the wrong archive is a mistake an
# operator should be able to walk back.
ASIDE=/var/lib/hlab/restore-__STAMP__
if [ -d __DIR__ ]; then
  $SUDO mkdir -p /var/lib/hlab
  $SUDO rm -rf "$ASIDE"
  $SUDO cp -a __DIR__ "$ASIDE"
  echo "kept:$ASIDE"
fi

$SUDO mkdir -p __PARENT__
$SUDO tar -xzf "$ARCHIVE" -C __PARENT__

if command -v docker >/dev/null 2>&1; then
  for d in __DIR__/*/; do
    [ -f "$d/compose.yaml" ] || continue
    printf 'started:%s\n' "$(basename "$d")"
    ( cd "$d" && $SUDO docker compose up -d >/dev/null 2>&1 ) || true
  done
fi

echo '__M_RESTORED__'
`

// BuildRestoreScript renders the script that puts a backup back.
func BuildRestoreScript(sha256sum, stamp string) (string, error) {
	if len(sha256sum) != 64 {
		return "", fmt.Errorf("checksum %q is not a sha256 digest", sha256sum)
	}
	for _, r := range sha256sum {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return "", fmt.Errorf("checksum %q is not hexadecimal", sha256sum)
		}
	}
	for _, r := range stamp {
		if !(r >= '0' && r <= '9' || r == '-') {
			return "", fmt.Errorf("invalid backup stamp %q", stamp)
		}
	}

	parent, _ := split(RemoteDir)
	return strings.NewReplacer(
		"__DIR__", RemoteDir,
		"__PARENT__", shellword.Quote(parent),
		"__SUM__", shellword.Quote(sha256sum),
		"__STAMP__", stamp,
		"__M_RESTORED__", MarkerRestored,
	).Replace(restoreTemplate), nil
}

// split separates a path into its parent and final element.
func split(path string) (parent, base string) {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "/", strings.TrimPrefix(path, "/")
	}
	return path[:i], path[i+1:]
}
