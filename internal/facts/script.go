package facts

// SectionPrefix delimits the sections of the collection script's output.
const SectionPrefix = "##HLAB#"

// CollectScript is run on each host to gather a snapshot in a single round
// trip. It is deliberately POSIX sh with no bashisms, and every tool it uses is
// probed with `command -v` first, so it degrades cleanly on minimal systems
// (Alpine, busybox, appliances) instead of erroring out.
//
// Nothing here writes, installs or configures anything: it only reads.
// It also never uses sudo — whatever an unprivileged login can see is what gets
// reported, which means process names against listening ports may be missing
// unless the configured user is root.
const CollectScript = `
LC_ALL=C
export LC_ALL

s() { printf '` + SectionPrefix + `%s#\n' "$1"; }

# Some of what is worth knowing is only visible to root: the process behind a
# listening socket, and the container list on a host where the management
# account is not in the docker group.
#
# SUDO is used only where it changes the answer, and only when it can be had
# without a prompt — a machine adopted with an unprivileged account still
# reports everything it can rather than hanging or failing.
SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

s privileged
if [ "$(id -u 2>/dev/null)" = 0 ]; then
  echo root
elif [ -n "$SUDO" ]; then
  echo sudo
else
  echo none
fi

s hostname
{ hostname 2>/dev/null || cat /proc/sys/kernel/hostname 2>/dev/null; } | head -n 1

s os-release
cat /etc/os-release 2>/dev/null

s kernel
uname -r 2>/dev/null

s arch
uname -m 2>/dev/null

s uptime
cat /proc/uptime 2>/dev/null

s cpu
{ nproc 2>/dev/null || grep -c '^processor' /proc/cpuinfo 2>/dev/null; } | head -n 1

s memory
grep -E '^(MemTotal|MemAvailable):' /proc/meminfo 2>/dev/null

s load
cat /proc/loadavg 2>/dev/null

s disk
df -PT 2>/dev/null

s init
if command -v systemctl >/dev/null 2>&1; then
  echo systemd
elif command -v rc-status >/dev/null 2>&1; then
  echo openrc
else
  echo unknown
fi

s services
if command -v systemctl >/dev/null 2>&1; then
  systemctl list-units --type=service --state=running --no-pager --no-legend --plain 2>/dev/null
elif command -v rc-status >/dev/null 2>&1; then
  rc-status -s 2>/dev/null
fi

s interfaces
if command -v ip >/dev/null 2>&1; then
  ip -o link show 2>/dev/null
  ip -o addr show 2>/dev/null
elif command -v ifconfig >/dev/null 2>&1; then
  ifconfig -a 2>/dev/null
fi

s interface-detail
# /sys is the authoritative source for what an interface *is* and whether its
# link is actually up — naming conventions are a guess, this is not.
for d in /sys/class/net/*; do
  [ -e "$d" ] || continue
  n=${d##*/}
  if [ -e "$d/phy80211" ]; then
    t=wifi
  elif [ -d "$d/bridge" ]; then
    t=bridge
  elif [ "$n" = lo ]; then
    t=loopback
  elif [ -e "$d/device" ]; then
    t=ethernet
  else
    t=virtual
  fi
  # carrier reads EINVAL while an interface is down, hence the guard.
  c=$(cat "$d/carrier" 2>/dev/null || echo '')
  o=$(cat "$d/operstate" 2>/dev/null || echo '')
  sp=$(cat "$d/speed" 2>/dev/null || echo '')
  printf '%s|%s|%s|%s|%s\n' "$n" "$t" "$c" "$o" "$sp"
done

s netmanager
# Which tool owns the configuration decides how it can later be changed.
if command -v systemctl >/dev/null 2>&1; then
  for svc in NetworkManager systemd-networkd dhcpcd networking; do
    if systemctl is-active "$svc" >/dev/null 2>&1; then
      echo "service=$svc"
    fi
  done
fi
# netplan counts only when the tool is installed and owns files that are not
# NetworkManager.s own output: NM emits /etc/netplan/90-NM-*.yaml as a report
# of what it is doing, not as configuration it reads back.
if command -v netplan >/dev/null 2>&1; then
  for f in /etc/netplan/*.yaml; do
    [ -f "$f" ] || continue
    case "${f##*/}" in
      90-NM-*) ;;
      *) echo "config=netplan"; break ;;
    esac
  done
fi
[ -f /etc/dhcpcd.conf ] && echo 'config=dhcpcd'
[ -f /etc/network/interfaces ] && echo 'config=ifupdown'
ls /etc/systemd/network/*.network >/dev/null 2>&1 && echo 'config=systemd-networkd'

s gateway
if command -v ip >/dev/null 2>&1; then
  ip route show default 2>/dev/null
elif command -v route >/dev/null 2>&1; then
  route -n 2>/dev/null
fi

s dns
grep -E '^[[:space:]]*(nameserver|search|domain)' /etc/resolv.conf 2>/dev/null

s ports
if command -v ss >/dev/null 2>&1; then
  $SUDO ss -H -tlnp 2>/dev/null || ss -H -tln 2>/dev/null
elif command -v netstat >/dev/null 2>&1; then
  $SUDO netstat -tlnp 2>/dev/null || netstat -tln 2>/dev/null
fi

s containers
if command -v docker >/dev/null 2>&1; then
  # Unprivileged first: it succeeds when the account is in the docker group,
  # and avoids sudo where it is not needed. Duplicates are dropped on parse.
  docker ps --format 'docker|{{.Names}}|{{.Image}}|{{.Status}}' 2>/dev/null
  if [ -n "$SUDO" ]; then
    $SUDO docker ps --format 'docker|{{.Names}}|{{.Image}}|{{.Status}}' 2>/dev/null
  fi
fi
if command -v podman >/dev/null 2>&1; then
  # Rootless podman containers belong to the invoking user, so both views
  # matter here.
  podman ps --format 'podman|{{.Names}}|{{.Image}}|{{.Status}}' 2>/dev/null
  if [ -n "$SUDO" ]; then
    $SUDO podman ps --format 'podman|{{.Names}}|{{.Image}}|{{.Status}}' 2>/dev/null
  fi
fi

s end
`
