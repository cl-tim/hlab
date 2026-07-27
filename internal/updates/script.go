// Package updates reports and applies operating system package upgrades and
// container image refreshes across a fleet.
//
// Everything here is split into a read that changes nothing and an apply that
// is explicit about what it will do, because an upgrade is the one routine
// operation that can take a working machine off the network.
package updates

import "strings"

// SectionPrefix delimits the sections of the check script's output.
const SectionPrefix = "##HLAB#"

// Markers the apply script prints so the outcome can be reported precisely.
const (
	MarkerUpgraded = "hlab:upgraded"
	MarkerNoop     = "hlab:nothing-to-do"
)

// CheckScript reports what is out of date without changing anything that runs.
//
// It refreshes the package index, which writes to the package cache and
// nothing else — asking what is upgradable is meaningless against a stale
// index. No package is installed, no container is touched.
//
// POSIX sh only, and every tool is probed before use, so a machine without apt
// or without docker reports fewer sections rather than failing.
const CheckScript = `
LC_ALL=C
export LC_ALL

s() { printf '` + SectionPrefix + `%s#\n' "$1"; }

# The package index and the container list are both root-only reads on most
# machines. Sudo is used where it changes the answer and only when it can be
# had without a prompt.
SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

s manager
if command -v apt-get >/dev/null 2>&1; then
  echo apt
elif command -v apk >/dev/null 2>&1; then
  echo apk
elif command -v dnf >/dev/null 2>&1; then
  echo dnf
elif command -v pacman >/dev/null 2>&1; then
  echo pacman
else
  echo unknown
fi

s refresh
if command -v apt-get >/dev/null 2>&1; then
  if $SUDO apt-get -qq update >/dev/null 2>&1; then echo ok; else echo failed; fi
fi

s packages
# What an upgrade would actually install. This is the authoritative list: it
# respects held packages and anything apt would decline to touch.
if command -v apt-get >/dev/null 2>&1; then
  $SUDO apt-get -s upgrade 2>/dev/null | sed -n 's/^Inst \([^ ]*\) \[[^]]*\] (\([^ ]*\) .*/\1 \2/p'
fi

s upgradable
# Everything apt considers newer, including what "upgrade" would hold back
# because it would need to remove something. The difference between this and
# the section above is worth reporting rather than hiding.
if command -v apt-get >/dev/null 2>&1; then
  $SUDO apt list --upgradable 2>/dev/null | sed -n 's#^\([^/]*\)/\([^ ]*\) .*#\1 \2#p'
fi

s reboot
if [ -f /var/run/reboot-required ]; then
  echo required
  cat /var/run/reboot-required.pkgs 2>/dev/null
else
  echo no
fi

s kernel
uname -r 2>/dev/null

s containers
# A container still running an image that has since been replaced locally is
# behind its own stack. This is a purely local comparison — no registry is
# contacted, so the check stays a read.
if command -v docker >/dev/null 2>&1; then
  for c in $($SUDO docker ps -q 2>/dev/null); do
    name=$($SUDO docker inspect -f '{{.Name}}' "$c" 2>/dev/null | sed 's#^/##')
    tag=$($SUDO docker inspect -f '{{.Config.Image}}' "$c" 2>/dev/null)
    running=$($SUDO docker inspect -f '{{.Image}}' "$c" 2>/dev/null)
    current=$($SUDO docker image inspect -f '{{.Id}}' "$tag" 2>/dev/null)
    [ -n "$name" ] || continue
    if [ -n "$current" ] && [ "$running" != "$current" ]; then
      printf '%s %s stale\n' "$name" "$tag"
    else
      printf '%s %s current\n' "$name" "$tag"
    fi
  done
fi

s stacks
if command -v docker >/dev/null 2>&1; then
  for d in /opt/hlab/stacks/*/; do
    [ -f "$d/compose.yaml" ] || continue
    basename "$d"
  done
fi

s end
`

// upgradeTemplate installs pending package upgrades.
//
// `upgrade` rather than `full-upgrade`: it will never remove a package to
// satisfy a dependency. Anything that would need a removal is held back and
// reported instead, which is the right default for a machine you are not
// standing next to.
const upgradeTemplate = `
set -eu
export DEBIAN_FRONTEND=noninteractive

SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

if ! command -v apt-get >/dev/null 2>&1; then
  echo 'hlab:error only apt is supported for upgrades so far' >&2
  exit 40
fi

$SUDO apt-get -qq update >/dev/null 2>&1 || true

PENDING=$($SUDO apt-get -s __NEWPKGS__ upgrade 2>/dev/null | grep -c '^Inst ' || true)
if [ "$PENDING" = 0 ]; then
  echo '__M_NOOP__'
  exit 0
fi

# --force-confold keeps the configuration files already on the machine. Without
# it dpkg asks what to do about a changed config and waits forever for an
# answer nobody is there to give.
$SUDO apt-get -y __NEWPKGS__ \
  -o Dpkg::Options::=--force-confdef \
  -o Dpkg::Options::=--force-confold \
  upgrade

echo '__M_UPGRADED__'

if [ -f /var/run/reboot-required ]; then
  echo 'reboot:required'
  cat /var/run/reboot-required.pkgs 2>/dev/null
fi
`

// BuildUpgradeScript renders the package upgrade script.
//
// allowNew adds --with-new-pkgs, which lets apt install packages that did not
// previously exist while still never removing one. It is what kernel updates
// need: bumping linux-image-generic means installing a new linux-image-<ver>
// package, so a plain `upgrade` holds the kernel back indefinitely and the
// machine never receives a kernel security fix. It is not the default because
// installing new packages is a bigger step than updating existing ones, and
// the operator should say so.
func BuildUpgradeScript(allowNew bool) string {
	newPkgs := ""
	if allowNew {
		newPkgs = "--with-new-pkgs"
	}
	return strings.NewReplacer(
		"__NEWPKGS__", newPkgs,
		"__M_UPGRADED__", MarkerUpgraded,
		"__M_NOOP__", MarkerNoop,
	).Replace(upgradeTemplate)
}

// imageTemplate refreshes the images of the stacks hlab manages and recreates
// anything whose image moved.
//
// Only hlab's own stacks are touched. A hand-made project elsewhere on the
// machine has no reviewed compose file here, so pulling and recreating it
// would be acting on a definition nobody has looked at.
const imageTemplate = `
set -eu

SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

if ! command -v docker >/dev/null 2>&1; then
  echo 'hlab:error docker is not installed here' >&2
  exit 41
fi

FOUND=0
for d in /opt/hlab/stacks/*/; do
  [ -f "$d/compose.yaml" ] || continue
  FOUND=1
  name=$(basename "$d")
  printf 'stack:%s\n' "$name"
  cd "$d"
  # pull then up: pull alone changes nothing that is running, and up only
  # recreates the containers whose image actually moved.
  $SUDO docker compose pull 2>&1 | sed 's/^/  /'
  $SUDO docker compose up -d 2>&1 | sed 's/^/  /'
done

if [ "$FOUND" = 0 ]; then
  echo '__M_NOOP__'
  exit 0
fi
echo '__M_UPGRADED__'
`

// BuildImageScript renders the container image refresh script.
func BuildImageScript() string {
	return strings.NewReplacer(
		"__M_UPGRADED__", MarkerUpgraded,
		"__M_NOOP__", MarkerNoop,
	).Replace(imageTemplate)
}
