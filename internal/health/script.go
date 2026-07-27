// Package health asserts that machines are actually working, rather than
// merely reachable.
//
// The gap this fills is specific: SSH answering says nothing about whether the
// service the machine exists to run is doing its job. A container can sit in
// "Up" for days while failing every request, a filesystem can fill to the point
// where the next write loses data, and a systemd unit can be dead without
// anything saying so.
package health

// SectionPrefix delimits the sections of the check script's output.
const SectionPrefix = "##HLAB#"

// CheckScript gathers the signals that say whether a machine is healthy.
//
// It only reads. POSIX sh, every tool probed before use, so a machine without
// docker or without systemd reports fewer sections rather than failing.
const CheckScript = `
LC_ALL=C
export LC_ALL

s() { printf '` + SectionPrefix + `%s#\n' "$1"; }

SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

s disk
# Local, real filesystems only.
#
# tmpfs and overlay fill and empty on their own and would report a machine as
# sick every time a container wrote a log line. Network filesystems are
# excluded for a different reason: a full NAS is a real problem but it is not
# this machine's, and reporting it here means every machine that mounts the
# share cries about it at once.
df -PTh 2>/dev/null | awk 'NR>1 && $2 !~ /^(tmpfs|devtmpfs|overlay|squashfs|efivarfs|cifs|nfs|nfs4|smbfs|fuse.sshfs|autofs)$/ {
  gsub(/%/,"",$6); print $7, $6, $5
}'

s units
if command -v systemctl >/dev/null 2>&1; then
  systemctl list-units --state=failed --no-legend --plain 2>/dev/null | awk '{print $1}'
fi

s containers
# State, health and restart count together. "Up" alone is not health: a
# container can be up and failing, and one that is up because it just restarted
# for the ninth time is not working either.
if command -v docker >/dev/null 2>&1; then
  for c in $($SUDO docker ps -aq 2>/dev/null); do
    $SUDO docker inspect -f '{{.Name}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}-{{end}}|{{.RestartCount}}' "$c" 2>/dev/null | sed 's#^/##'
  done
fi

s stacks
# A stack whose compose file lists more services than are running has lost one,
# which "docker ps" alone will not tell you because the missing one is absent.
if command -v docker >/dev/null 2>&1; then
  for d in /opt/hlab/stacks/*/; do
    [ -f "$d/compose.yaml" ] || continue
    name=$(basename "$d")
    want=$(cd "$d" && $SUDO docker compose config --services 2>/dev/null | wc -l | tr -d ' ')
    have=$(cd "$d" && $SUDO docker compose ps --status running --format '{{.Service}}' 2>/dev/null | wc -l | tr -d ' ')
    printf '%s %s %s\n' "$name" "$want" "$have"
  done
fi

s reboot
if [ -f /var/run/reboot-required ]; then echo required; else echo no; fi

s clock
# A machine whose clock has drifted breaks TLS and anything else that checks a
# validity window, and it does so in ways that look like unrelated faults.
if command -v timedatectl >/dev/null 2>&1; then
  timedatectl show -p NTPSynchronized --value 2>/dev/null
fi

s uptime
cat /proc/uptime 2>/dev/null | cut -d' ' -f1

s end
`
