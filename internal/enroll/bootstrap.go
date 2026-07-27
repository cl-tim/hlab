package enroll

import (
	"fmt"
	"regexp"
	"strings"
)

// SudoMode controls what the management account is allowed to do.
type SudoMode string

const (
	// SudoNoPassword grants passwordless sudo. This is what makes unattended
	// management possible, and it means possession of hlab's private key is
	// equivalent to root on the machine.
	SudoNoPassword SudoMode = "nopasswd"
	// SudoPassword requires a password for sudo. Safer, but the account cannot
	// then run privileged commands non-interactively.
	SudoPassword SudoMode = "password"
	// SudoNone grants no sudo at all: an unprivileged read-only account.
	SudoNone SudoMode = "none"
)

// ParseSudoMode validates a user-supplied sudo mode.
func ParseSudoMode(s string) (SudoMode, error) {
	switch SudoMode(strings.ToLower(strings.TrimSpace(s))) {
	case SudoNoPassword:
		return SudoNoPassword, nil
	case SudoPassword:
		return SudoPassword, nil
	case SudoNone:
		return SudoNone, nil
	default:
		return "", fmt.Errorf("unknown sudo mode %q (want one of: nopasswd, password, none)", s)
	}
}

// validUsername matches the portable account name syntax useradd accepts.
var validUsername = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ValidateUsername rejects names that are unsafe or that useradd would refuse.
// The name is interpolated into a shell script, so this is a security control
// as well as a usability one.
func ValidateUsername(name string) error {
	if !validUsername.MatchString(name) {
		return fmt.Errorf(
			"invalid account name %q: use lowercase letters, digits, underscore and dash, starting with a letter or underscore", name)
	}
	return nil
}

// Markers the bootstrap script prints so the outcome can be reported precisely.
const (
	markerUserCreated    = "hlab:user-created"
	markerUserExists     = "hlab:user-exists"
	markerSudoConfigured = "hlab:sudo-configured"
	markerSudoSkipped    = "hlab:sudo-skipped"
	markerKeyInstalled   = "hlab:key-installed"
	markerKeyPresent     = "hlab:key-present"
	markerDone           = "hlab:done"
)

// bootstrapTemplate provisions the management account. It runs as root.
//
// Everything is idempotent: re-running against an already-enrolled machine
// reports what was already in place rather than failing or duplicating.
//
// The sudoers fragment is written to a temporary file and validated with
// `visudo -c` before being moved into place, because installing a malformed
// sudoers file can make sudo unusable on the machine.
const bootstrapTemplate = `
set -eu

HLAB_USER=__USER__
HLAB_KEY=__KEY__
SUDO_MODE=__SUDO_MODE__

# --- account -----------------------------------------------------------
if id -u "$HLAB_USER" >/dev/null 2>&1; then
  echo '__M_USER_EXISTS__'
else
  LOGIN_SHELL=/bin/sh
  [ -x /bin/bash ] && LOGIN_SHELL=/bin/bash
  if command -v useradd >/dev/null 2>&1; then
    useradd --create-home --shell "$LOGIN_SHELL" --comment 'hlab management account' "$HLAB_USER"
  elif command -v adduser >/dev/null 2>&1; then
    adduser -D -s "$LOGIN_SHELL" "$HLAB_USER"
  else
    echo 'hlab:error no useradd or adduser on this system' >&2
    exit 10
  fi
  echo '__M_USER_CREATED__'
fi

# The account authenticates by key only; make sure it has no usable password.
if command -v passwd >/dev/null 2>&1; then
  passwd -l "$HLAB_USER" >/dev/null 2>&1 || true
fi

# --- sudo --------------------------------------------------------------
if [ "$SUDO_MODE" = none ]; then
  rm -f /etc/sudoers.d/60-hlab
  echo '__M_SUDO_SKIPPED__'
else
  if [ ! -d /etc/sudoers.d ]; then
    echo 'hlab:error /etc/sudoers.d does not exist; is sudo installed?' >&2
    exit 11
  fi

  SUDO_TMP=$(mktemp)
  if [ "$SUDO_MODE" = nopasswd ]; then
    printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$HLAB_USER" > "$SUDO_TMP"
  else
    printf '%s ALL=(ALL) ALL\n' "$HLAB_USER" > "$SUDO_TMP"
  fi
  chmod 0440 "$SUDO_TMP"

  # Validate before installing: a broken sudoers file can lock sudo out.
  if command -v visudo >/dev/null 2>&1; then
    if ! visudo -c -f "$SUDO_TMP" >/dev/null 2>&1; then
      rm -f "$SUDO_TMP"
      echo 'hlab:error generated sudoers fragment failed validation' >&2
      exit 12
    fi
  fi

  mv "$SUDO_TMP" /etc/sudoers.d/60-hlab
  chown root:root /etc/sudoers.d/60-hlab 2>/dev/null || true
  chmod 0440 /etc/sudoers.d/60-hlab
  echo '__M_SUDO_CONFIGURED__'
fi

# --- authorized key ----------------------------------------------------
HOME_DIR=$(getent passwd "$HLAB_USER" 2>/dev/null | cut -d: -f6 || true)
[ -n "${HOME_DIR:-}" ] || HOME_DIR="/home/$HLAB_USER"

mkdir -p "$HOME_DIR/.ssh"
chmod 700 "$HOME_DIR/.ssh"
AUTH_KEYS="$HOME_DIR/.ssh/authorized_keys"
touch "$AUTH_KEYS"
chmod 600 "$AUTH_KEYS"

if grep -qF "$HLAB_KEY" "$AUTH_KEYS" 2>/dev/null; then
  echo '__M_KEY_PRESENT__'
else
  printf '%s\n' "$HLAB_KEY" >> "$AUTH_KEYS"
  echo '__M_KEY_INSTALLED__'
fi

HLAB_GROUP=$(id -gn "$HLAB_USER" 2>/dev/null || echo "$HLAB_USER")
chown -R "$HLAB_USER:$HLAB_GROUP" "$HOME_DIR/.ssh"

echo '__M_DONE__'
`

// BuildBootstrapScript renders the provisioning script for one machine.
func BuildBootstrapScript(username, authorizedKey string, mode SudoMode) (string, error) {
	if err := ValidateUsername(username); err != nil {
		return "", err
	}
	if strings.TrimSpace(authorizedKey) == "" {
		return "", fmt.Errorf("no authorized key to install")
	}
	// A newline in the key would let it forge extra authorized_keys entries.
	if strings.ContainsAny(authorizedKey, "\n\r") {
		return "", fmt.Errorf("authorized key contains a line break")
	}

	return strings.NewReplacer(
		"__USER__", shellQuote(username),
		"__KEY__", shellQuote(authorizedKey),
		"__SUDO_MODE__", shellQuote(string(mode)),
		"__M_USER_CREATED__", markerUserCreated,
		"__M_USER_EXISTS__", markerUserExists,
		"__M_SUDO_CONFIGURED__", markerSudoConfigured,
		"__M_SUDO_SKIPPED__", markerSudoSkipped,
		"__M_KEY_INSTALLED__", markerKeyInstalled,
		"__M_KEY_PRESENT__", markerKeyPresent,
		"__M_DONE__", markerDone,
	).Replace(bootstrapTemplate), nil
}

// shellQuote renders a value as a single-quoted shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
