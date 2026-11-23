package tlsmgr

import (
	"encoding/base64"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

const RootPath = "/var/lib/hlab-tls/caddy/pki/authorities/hlab/root.crt"

// Preflight is read-only and runs before downloading or changing a host. An
// existing listener is only updated when it belongs to this managed daemon.
func (c Config) Preflight(target string) (string, error) {
	c = c.Defaults()
	if err := c.Validate(); err != nil {
		return "", err
	}
	ports := map[int]bool{}
	if target == "host:"+c.Authority {
		ports[c.AuthorityPort] = true
	}
	for _, s := range c.Services {
		if s.Target == target {
			ports[s.Port] = true
			if target != "host:"+c.Authority {
				ports[443] = true
			}
		}
	}
	var sorted []int
	for p := range ports {
		sorted = append(sorted, p)
	}
	sort.Ints(sorted)
	script := `set -eu
command -v systemctl >/dev/null
command -v ss >/dev/null
command -v openssl >/dev/null
command -v base64 >/dev/null
`
	script += `if test -f /etc/hlab/tls/Caddyfile; then
 owner=$(sed -n 's/^# hlab target: //p' /etc/hlab/tls/Caddyfile)
 if [ -n "$owner" ] && [ "$owner" != ` + quote(target) + ` ]; then
  echo 'HTTPS on this device belongs to a different inventory target; choose one host: or printer: target per device' >&2; exit 1
 fi
fi
`
	script += `
if ! command -v update-ca-certificates >/dev/null 2>&1 && ! command -v update-ca-trust >/dev/null 2>&1; then
  echo 'No supported system trust store' >&2; exit 1
fi
if ! systemctl is-active --quiet hlab-tls.service; then
`
	for _, p := range sorted {
		script += fmt.Sprintf("  if ss -lntH 'sport = :%d' | grep -q .; then echo 'Port %d is occupied; existing listeners will not be replaced' >&2; exit 1; fi\n", p, p)
	}
	script += "fi\nuname -m\ngetconf LONG_BIT\n"
	return script, nil
}

const Unit = `[Unit]
Description=hlab device HTTPS and private PKI
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=notify
User=hlab-tls
Group=hlab-tls
Environment=HOME=/var/lib/hlab-tls
Environment=XDG_DATA_HOME=/var/lib/hlab-tls
Environment=XDG_CONFIG_HOME=/var/lib/hlab-tls/config
ExecStart=/usr/local/lib/hlab/caddy run --config /etc/hlab/tls/Caddyfile --adapter caddyfile
ExecReload=/usr/local/lib/hlab/caddy reload --config /etc/hlab/tls/Caddyfile --adapter caddyfile --address unix//run/hlab-tls/admin.sock
Restart=on-failure
RestartSec=5s
TimeoutStopSec=20s
LimitNOFILE=65536
UMask=0077
StateDirectory=hlab-tls
StateDirectoryMode=0700
RuntimeDirectory=hlab-tls
RuntimeDirectoryMode=0700
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/hlab-tls /run/hlab-tls
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6

[Install]
WantedBy=multi-user.target
`

// InstallScript stages a candidate, checks syntax, then reloads transactionally.
// Reload failures restore the previous file and leave the old daemon serving.
// The managed unit and binary are isolated from distro/vendor web services.
func InstallScript(rendered string, root []byte, resolvers []string) (string, error) {
	if len(root) > 0 {
		if _, _, err := Root(root); err != nil {
			return "", err
		}
	}
	script := `set -eu
umask 077
getent group hlab-tls >/dev/null || groupadd --system hlab-tls
id hlab-tls >/dev/null 2>&1 || useradd --system --gid hlab-tls --home-dir /var/lib/hlab-tls --shell /usr/sbin/nologin hlab-tls
install -d -m 755 /etc/hlab /etc/hlab/tls
install -d -o hlab-tls -g hlab-tls -m 700 /var/lib/hlab-tls
install -d -o hlab-tls -g hlab-tls -m 700 /run/hlab-tls
stage=$(mktemp -d /etc/hlab/tls/.stage.XXXXXXXX)
trap 'rm -rf "$stage"' EXIT
`
	if len(root) > 0 {
		script += "printf %s " + quote(base64.StdEncoding.EncodeToString(root)) + " | base64 -d >\"$stage/root.crt\"\ninstall -m 644 \"$stage/root.crt\" /etc/hlab/tls/root.crt\n"
	}
	script += "printf %s " + quote(base64.StdEncoding.EncodeToString([]byte(rendered))) + " | base64 -d >\"$stage/Caddyfile\"\n"
	// adapt validates syntax without opening a second ACME database; reload
	// provisions modules inside the running daemon, where rejection is atomic.
	script += `/usr/local/lib/hlab/caddy adapt --config "$stage/Caddyfile" --adapter caddyfile >/dev/null
changed=false
if ! cmp -s "$stage/Caddyfile" /etc/hlab/tls/Caddyfile; then
  if test -f /etc/hlab/tls/Caddyfile; then cp -p /etc/hlab/tls/Caddyfile /etc/hlab/tls/Caddyfile.previous; fi
  install -m 644 "$stage/Caddyfile" /etc/hlab/tls/Caddyfile
  changed=true
fi
`
	unit := Unit
	if len(resolvers) > 0 {
		var resolv strings.Builder
		for _, r := range resolvers {
			if net.ParseIP(r) == nil {
				return "", fmt.Errorf("invalid TLS resolver %q", r)
			}
			fmt.Fprintf(&resolv, "nameserver %s\n", r)
		}
		resolv.WriteString("options timeout:2 attempts:2\n")
		script += "printf %s " + quote(base64.StdEncoding.EncodeToString([]byte(resolv.String()))) + " | base64 -d >\"$stage/resolv.conf\"\ninstall -m 644 \"$stage/resolv.conf\" /etc/hlab/tls/resolv.conf\n"
		unit = strings.Replace(unit, "[Service]\n", "[Service]\nBindReadOnlyPaths=/etc/hlab/tls/resolv.conf:/etc/resolv.conf\n", 1)
	}
	script += "printf %s " + quote(base64.StdEncoding.EncodeToString([]byte(unit))) + " | base64 -d >\"$stage/hlab-tls.service\"\n"
	script += `unit_changed=false
if ! cmp -s "$stage/hlab-tls.service" /etc/systemd/system/hlab-tls.service; then
  install -m 644 "$stage/hlab-tls.service" /etc/systemd/system/hlab-tls.service
  systemctl daemon-reload
  unit_changed=true
fi
if systemctl is-active --quiet hlab-tls.service; then
  if [ "$changed" = true ]; then
    if ! systemctl reload hlab-tls.service; then
      if test -f /etc/hlab/tls/Caddyfile.previous; then cp -p /etc/hlab/tls/Caddyfile.previous /etc/hlab/tls/Caddyfile; fi
      echo 'Caddy reload rejected; previous configuration restored' >&2; exit 1
    fi
  fi
  if [ "$unit_changed" = true ]; then systemctl restart hlab-tls.service; fi
else
  systemctl start hlab-tls.service
fi
systemctl enable hlab-tls.service >/dev/null
pid=$(systemctl show --property MainPID --value hlab-tls.service)
case "$(readlink "/proc/$pid/exe")" in
  *' (deleted)') systemctl restart hlab-tls.service ;;
esac
systemctl is-active --quiet hlab-tls.service
printf 'hlab HTTPS active\n'
`
	return script, nil
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// BinaryCommand consumes a verified binary through SSH stdin; no binary or
// credential is embedded in the shell command line or stored in a user temp.
func BinaryCommand(digest string) string {
	return `sudo -n sh -c 'set -eu; umask 077; install -d -m 755 /usr/local/lib/hlab; stage=$(mktemp /usr/local/lib/hlab/.caddy.XXXXXXXX); trap '\''rm -f "$stage"'\'' EXIT; cat >"$stage"; echo "` + digest + `  $stage" | sha256sum -c - >/dev/null; chmod 755 "$stage"; mv "$stage" /usr/local/lib/hlab/caddy'`
}

func (c Config) Ports(target string) []string {
	seen := map[int]bool{}
	if target == "host:"+c.Authority {
		seen[c.Defaults().AuthorityPort] = true
	}
	for _, s := range c.Defaults().Services {
		if s.Target == target {
			seen[s.Port] = true
		}
	}
	var ports []string
	for p := range seen {
		ports = append(ports, strconv.Itoa(p))
	}
	sort.Strings(ports)
	return ports
}
