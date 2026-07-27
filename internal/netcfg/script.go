package netcfg

import (
	"fmt"
	"strings"
)

// Markers the remote scripts print so the outcome can be reported precisely.
const (
	MarkerApplied = "hlab:applied"
	MarkerArmed   = "hlab:armed"
	MarkerNoop    = "hlab:noop"
)

// ConfirmPath is touched to tell the dead-man timer the change worked.
const ConfirmPath = "/run/hlab-netcfg-confirmed"

// RollbackSeconds is how long the machine waits for confirmation before
// putting the old configuration back.
//
// It has to outlast a DHCP-to-static transition plus a fresh SSH handshake,
// and be short enough that an operator is not left waiting on a machine that
// has already gone. Ninety seconds covers both.
const RollbackSeconds = 90

// shellQuote renders a value as a single-quoted shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// netplanYAML renders a netplan file pinning one interface to a static address.
func netplanYAML(iface, address, gateway string, dns, search []string) string {
	var b strings.Builder
	b.WriteString("# Written by hlab. Edits here are replaced on the next change.\n")
	b.WriteString("network:\n  version: 2\n  ethernets:\n")
	fmt.Fprintf(&b, "    %s:\n", iface)
	b.WriteString("      dhcp4: false\n")
	fmt.Fprintf(&b, "      addresses: [%s]\n", address)
	b.WriteString("      routes:\n")
	fmt.Fprintf(&b, "        - to: default\n          via: %s\n", gateway)
	b.WriteString("      nameservers:\n")
	fmt.Fprintf(&b, "        addresses: [%s]\n", strings.Join(dns, ", "))
	return b.String()
}

// dhcpcdConf renders the dhcpcd stanza pinning one interface.
func dhcpcdConf(iface, address, gateway string, dns, search []string) string {
	var b strings.Builder
	b.WriteString("\n# --- hlab static configuration; do not edit between these markers ---\n")
	fmt.Fprintf(&b, "interface %s\n", iface)
	fmt.Fprintf(&b, "static ip_address=%s\n", address)
	fmt.Fprintf(&b, "static routers=%s\n", gateway)
	fmt.Fprintf(&b, "static domain_name_servers=%s\n", strings.Join(dns, " "))
	b.WriteString("# --- end hlab static configuration ---\n")
	return b.String()
}

// staticScriptTemplate installs a static address behind a dead-man timer.
//
// The order matters: back up, arm the revert, then apply. If the apply cuts the
// connection — which is exactly what a wrong address does — the timer is
// already running and the machine restores itself without anyone reaching it.
const staticScriptTemplate = `
set -eu

CONFIRM=__CONFIRM__
BACKUP_DIR=/var/lib/hlab/netcfg
mkdir -p "$BACKUP_DIR"
rm -f "$CONFIRM"

MANAGER=__MANAGER__
IFACE=__IFACE__
TARGET=__TARGET__
ADDR=__ADDR__
GW=__GW__
DNSLIST=__DNS__
SEARCH=__SEARCH__

# --- pre-flight -----------------------------------------------------------
# The manager was decided when the machine was last collected. Confirm the
# tooling is actually here before displacing anything: acting on a stale fact
# is how a machine loses its configuration for nothing.
if [ "$MANAGER" = netplan ] && ! command -v netplan >/dev/null 2>&1; then
  echo 'hlab:error netplan is not installed here; the recorded manager is wrong. Run hlab sync first' >&2
  exit 20
fi
if [ "$MANAGER" = NetworkManager ] && ! command -v nmcli >/dev/null 2>&1; then
  echo 'hlab:error nmcli is not installed here; the recorded manager is wrong. Run hlab sync first' >&2
  exit 21
fi
if [ "$MANAGER" = dhcpcd ] && [ ! -f /etc/dhcpcd.conf ]; then
  echo 'hlab:error /etc/dhcpcd.conf is missing; the recorded manager is wrong. Run hlab sync first' >&2
  exit 22
fi

CON=
if [ "$MANAGER" = NetworkManager ]; then
  CON=$(nmcli -g GENERAL.CONNECTION device show "$IFACE" 2>/dev/null | head -n 1)
  if [ -z "$CON" ]; then
    echo 'hlab:error no NetworkManager connection is active on that interface' >&2
    exit 23
  fi
fi

# --- back up what is there now ------------------------------------------
# The revert restores whatever backups it finds, so only one manager's may be
# left lying about: a machine that was once handled as netplan and is now
# handled through NM would otherwise have both put back.
rm -rf "$BACKUP_DIR/netplan" "$BACKUP_DIR/nm" "$BACKUP_DIR/dhcpcd.conf.bak"

if [ "$MANAGER" = netplan ]; then
  mkdir -p "$BACKUP_DIR/netplan"
  cp -a /etc/netplan/. "$BACKUP_DIR/netplan/" 2>/dev/null || true
elif [ "$MANAGER" = NetworkManager ]; then
  # NM is asked for its own settings rather than having its files copied.
  # Where a profile lives is a distribution decision: Debian and Ubuntu keep
  # keyfiles in /etc/NetworkManager/system-connections, but NM built with
  # netplan support writes /etc/netplan/90-NM-*.yaml instead and generates the
  # keyfile into /run, which is tmpfs. Copying the wrong directory yields an
  # empty backup, and an empty backup is a revert that silently restores
  # nothing. Asking nmcli works on all of them.
  #
  # One value per file so no quoting is needed to read them back.
  mkdir -p "$BACKUP_DIR/nm"
  printf '%s' "$CON" > "$BACKUP_DIR/nm/con"
  for k in method addresses gateway dns; do
    nmcli -g "ipv4.$k" connection show "$CON" > "$BACKUP_DIR/nm/$k" 2>/dev/null || : > "$BACKUP_DIR/nm/$k"
  done
else
  cp -a /etc/dhcpcd.conf "$BACKUP_DIR/dhcpcd.conf.bak" 2>/dev/null || true
fi

# --- arm the revert ------------------------------------------------------
# Detached from this session on purpose: it has to survive the connection
# dropping, which is the case it exists for.
cat > "$BACKUP_DIR/revert.sh" <<'REVERT'
#!/bin/sh
sleep __ROLLBACK__
[ -f __CONFIRM__ ] && exit 0
BACKUP_DIR=/var/lib/hlab/netcfg
if [ -d "$BACKUP_DIR/netplan" ]; then
  rm -f /etc/netplan/*.yaml 2>/dev/null || true
  cp -a "$BACKUP_DIR/netplan/." /etc/netplan/ 2>/dev/null || true
  netplan apply >/dev/null 2>&1 || true
fi
if [ -f "$BACKUP_DIR/nm/con" ]; then
  CON=$(cat "$BACKUP_DIR/nm/con")
  # Restored through NM by connection name, for the same reason the backup was
  # taken that way: the profile is not always in a file we could put back.
  nmcli connection modify "$CON" \
    ipv4.method "$(cat "$BACKUP_DIR/nm/method")" \
    ipv4.addresses "$(cat "$BACKUP_DIR/nm/addresses")" \
    ipv4.gateway "$(cat "$BACKUP_DIR/nm/gateway")" \
    ipv4.dns "$(cat "$BACKUP_DIR/nm/dns")" >/dev/null 2>&1 || true
  nmcli connection up "$CON" >/dev/null 2>&1 || true
fi
if [ -f "$BACKUP_DIR/dhcpcd.conf.bak" ]; then
  cp -a "$BACKUP_DIR/dhcpcd.conf.bak" /etc/dhcpcd.conf
  systemctl restart dhcpcd >/dev/null 2>&1 || service dhcpcd restart >/dev/null 2>&1 || true
fi
REVERT
chmod +x "$BACKUP_DIR/revert.sh"
setsid "$BACKUP_DIR/revert.sh" >/dev/null 2>&1 < /dev/null &
echo '__M_ARMED__'

# --- write the new configuration -----------------------------------------
# Only the files are written here. Nothing that touches the link runs in this
# session.
if [ "$MANAGER" = netplan ]; then
  printf '%s' __PAYLOAD__ > "$TARGET"
  chmod 0600 "$TARGET"
  # A machine can carry several netplan files; the others may still say dhcp4
  # for this interface, so anything else mentioning it is moved aside.
  for f in /etc/netplan/*.yaml; do
    [ "$f" = "$TARGET" ] && continue
    [ -f "$f" ] || continue
    if grep -q "$IFACE" "$f" 2>/dev/null; then
      mv "$f" "$f.hlab-disabled"
    fi
  done
elif [ "$MANAGER" = NetworkManager ]; then
  # Editing the stored profile changes nothing until it is brought up, so this
  # is safe to run in-session.
  nmcli connection modify "$CON" \
    ipv4.method manual \
    ipv4.addresses "$ADDR" \
    ipv4.gateway "$GW" \
    ipv4.dns "$DNSLIST" \
    ipv4.dns-search "$SEARCH"
else
  # Remove any previous hlab stanza before appending a new one.
  sed -i '/# --- hlab static configuration/,/# --- end hlab static configuration ---/d' /etc/dhcpcd.conf
  printf '%s' __PAYLOAD__ >> /etc/dhcpcd.conf
fi

# --- bring it up, detached ------------------------------------------------
# The activation takes down the connection this script is running over. A
# shell killed by that SIGHUP takes its children with it, so an in-session
# reactivation can be interrupted part-way: the address lands and the link is
# left in a state that answers ICMP but never completes a TCP handshake, which
# reads as "the machine did not come back" for a change that very nearly
# worked. Detached, the script reports and exits cleanly and the activation
# finishes on its own.
cat > "$BACKUP_DIR/activate.sh" <<ACTIVATE
#!/bin/sh
# Let the caller receive its output before the link goes.
sleep 2
if [ "$MANAGER" = netplan ]; then
  netplan apply
elif [ "$MANAGER" = NetworkManager ]; then
  nmcli connection up "$CON"
else
  systemctl restart dhcpcd || service dhcpcd restart
fi
ACTIVATE
chmod +x "$BACKUP_DIR/activate.sh"
setsid "$BACKUP_DIR/activate.sh" >/dev/null 2>&1 < /dev/null &

echo '__M_APPLIED__'
`

// BuildStaticScript renders the script that pins an interface to its address.
func BuildStaticScript(p Plan) (string, error) {
	if p.Interface == "" || p.Address == "" || p.Gateway == "" {
		return "", fmt.Errorf("incomplete plan for %s", p.Host.Name)
	}

	var payload, target string
	switch p.Manager {
	case "netplan":
		payload = netplanYAML(p.Interface, p.Address, p.Gateway, p.DNS, p.Search)
		target = "/etc/netplan/60-hlab-" + p.Interface + ".yaml"
	case "NetworkManager":
		// nmcli takes the settings as arguments, so no file is rendered.
		payload = ""
		target = ""
	case "dhcpcd":
		payload = dhcpcdConf(p.Interface, p.Address, p.Gateway, p.DNS, p.Search)
		target = "/etc/dhcpcd.conf"
	default:
		return "", fmt.Errorf("unsupported network manager %q", p.Manager)
	}

	return strings.NewReplacer(
		"__CONFIRM__", shellQuote(ConfirmPath),
		"__MANAGER__", shellQuote(p.Manager),
		"__IFACE__", shellQuote(p.Interface),
		"__TARGET__", shellQuote(target),
		"__PAYLOAD__", shellQuote(payload),
		"__ADDR__", shellQuote(p.Address),
		"__GW__", shellQuote(p.Gateway),
		"__DNS__", shellQuote(strings.Join(p.DNS, ",")),
		"__SEARCH__", shellQuote(strings.Join(p.Search, ",")),
		"__ROLLBACK__", fmt.Sprint(RollbackSeconds),
		"__M_ARMED__", MarkerArmed,
		"__M_APPLIED__", MarkerApplied,
	).Replace(staticScriptTemplate), nil
}

// wifiScriptTemplate blocks wireless behind the same dead-man timer.
//
// rfkill is used rather than editing the network configuration: it is
// manager-agnostic, survives a reboot through systemd-rfkill, and is undone by
// a single unblock rather than by restoring files.
const wifiScriptTemplate = `
set -eu

CONFIRM=__CONFIRM__
BACKUP_DIR=/var/lib/hlab/netcfg
mkdir -p "$BACKUP_DIR"
rm -f "$CONFIRM"

if ! command -v rfkill >/dev/null 2>&1; then
  echo 'hlab:error rfkill is not installed' >&2
  exit 10
fi

if rfkill list wifi 2>/dev/null | grep -q 'Soft blocked: yes'; then
  echo '__M_NOOP__'
  exit 0
fi

cat > "$BACKUP_DIR/revert-wifi.sh" <<'REVERT'
#!/bin/sh
sleep __ROLLBACK__
[ -f __CONFIRM__ ] && exit 0
rfkill unblock wifi >/dev/null 2>&1 || true
REVERT
chmod +x "$BACKUP_DIR/revert-wifi.sh"
setsid "$BACKUP_DIR/revert-wifi.sh" >/dev/null 2>&1 < /dev/null &
echo '__M_ARMED__'

rfkill block wifi
# systemd-rfkill persists the block across reboots.
systemctl enable systemd-rfkill >/dev/null 2>&1 || true

echo '__M_APPLIED__'
`

// BuildWiFiScript renders the script that blocks wireless.
func BuildWiFiScript(p Plan) (string, error) {
	if p.Interface == "" {
		return "", fmt.Errorf("incomplete plan for %s", p.Host.Name)
	}

	return strings.NewReplacer(
		"__CONFIRM__", shellQuote(ConfirmPath),
		"__ROLLBACK__", fmt.Sprint(RollbackSeconds),
		"__M_ARMED__", MarkerArmed,
		"__M_APPLIED__", MarkerApplied,
		"__M_NOOP__", MarkerNoop,
	).Replace(wifiScriptTemplate), nil
}

// ConfirmScript cancels the dead-man revert once a change has been verified.
const ConfirmScript = `
mkdir -p /run 2>/dev/null || true
touch ` + ConfirmPath + `
pkill -f /var/lib/hlab/netcfg/revert >/dev/null 2>&1 || true
echo ` + MarkerApplied + `
`
