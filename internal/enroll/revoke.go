package enroll

import (
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/cl-tim/homelab-cli/internal/shellword"
)

// AccountComment is the GECOS comment the bootstrap gives the account it
// creates. It is how a machine says "hlab made this account": revocation only
// ever deletes an account carrying it, because the account on an adopted
// machine is the operator's own.
const AccountComment = "hlab management account"

// SudoersFragment is the file the bootstrap installs. The name is hlab's alone.
const SudoersFragment = "/etc/sudoers.d/60-hlab"

// Markers the revocation script prints.
const (
	MarkerKeyRemoved      = "hlab:key-removed"
	MarkerKeyAbsent       = "hlab:key-absent"
	MarkerSudoRemoved     = "hlab:sudo-removed"
	MarkerSudoAbsent      = "hlab:sudo-absent"
	MarkerAccountRemoving = "hlab:account-removing"
	MarkerRevoked         = "hlab:revoked"
)

// RevokeOptions describe what to take away from one machine.
type RevokeOptions struct {
	// User is the account hlab logs in as.
	User string
	// KeyBlob is the base64 body of hlab's public key, or empty when this
	// workstation has no management key and so cannot have installed one.
	KeyBlob string
	// RemoveAccount deletes User as well. Refused on the machine unless the
	// account carries AccountComment.
	RemoveAccount bool
}

// revokeTemplate removes hlab's access from a machine. It runs as root.
//
// The key is matched by its base64 body, not by its comment, which anyone can
// edit, and it is removed from every account rather than only the one hlab logs
// in as: the point is that the key opens nothing on this machine afterwards.
// Files are rewritten in place so their owner and mode survive.
//
// The key goes before the sudoers fragment. If anything fails between the two,
// a machine without the key but with the fragment is unreachable through it; the
// other order leaves a key that still logs in.
//
// The account cannot be deleted from inside a session that is logged in as it:
// userdel refuses while the account has processes, and this script is one of
// them. Deletion is detached and runs after the session has gone, which is also
// why it cannot be confirmed — by then hlab has no way back in to look.
const revokeTemplate = `
set -eu

HLAB_USER=__USER__
KEY_BLOB=__BLOB__
REMOVE_ACCOUNT=__REMOVE__
OURS=__COMMENT__

entry=$(getent passwd "$HLAB_USER" 2>/dev/null || grep "^$HLAB_USER:" /etc/passwd || true)

# Checked before anything changes, so a refusal leaves the machine untouched.
if [ "$REMOVE_ACCOUNT" = 1 ]; then
  if [ -z "$entry" ]; then
    echo "hlab:error there is no account $HLAB_USER to remove" >&2
    exit 20
  fi
  uid=$(printf '%s' "$entry" | cut -d: -f3)
  comment=$(printf '%s' "$entry" | cut -d: -f5)
  if [ "$uid" = 0 ]; then
    echo "hlab:error $HLAB_USER is uid 0 and will not be removed" >&2
    exit 21
  fi
  if [ "$comment" != "$OURS" ]; then
    echo "hlab:error $HLAB_USER was not created by hlab (its comment is '$comment'), so it is left alone; drop --remove-account" >&2
    exit 22
  fi
  if ! command -v setsid >/dev/null 2>&1; then
    echo "hlab:error setsid is not available, so the account cannot be removed after this session ends" >&2
    exit 24
  fi
fi

# --- key ---------------------------------------------------------------
if [ -n "$KEY_BLOB" ]; then
  found=0
  homes=$({ getent passwd 2>/dev/null || cat /etc/passwd; } | cut -d: -f6 | sort -u)
  for home in $homes; do
    for f in "$home/.ssh/authorized_keys" "$home/.ssh/authorized_keys2"; do
      [ -f "$f" ] || continue
      grep -qF -- "$KEY_BLOB" "$f" || continue
      tmp=$(mktemp)
      # grep exits 1 when nothing is left, which is a valid result here.
      grep -vF -- "$KEY_BLOB" "$f" > "$tmp" || [ $? -eq 1 ]
      cat "$tmp" > "$f"
      rm -f "$tmp"
      if grep -qF -- "$KEY_BLOB" "$f"; then
        echo "hlab:error the key is still in $f after removing it" >&2
        exit 23
      fi
      echo "__M_KEY_REMOVED__ $f"
      found=1
    done
  done
  [ "$found" = 1 ] || echo '__M_KEY_ABSENT__'
else
  echo '__M_KEY_ABSENT__'
fi

# --- sudo --------------------------------------------------------------
if [ -e __SUDOERS__ ]; then
  rm -f __SUDOERS__
  echo '__M_SUDO_REMOVED__'
else
  echo '__M_SUDO_ABSENT__'
fi

# --- account -----------------------------------------------------------
if [ "$REMOVE_ACCOUNT" = 1 ]; then
  setsid sh -c '
    sleep 3
    if command -v pkill >/dev/null 2>&1; then
      pkill -KILL -u "$1" || true
    else
      for p in $(ps -o pid= -u "$1" 2>/dev/null); do kill -9 "$p" 2>/dev/null || true; done
    fi
    sleep 1
    if command -v userdel >/dev/null 2>&1; then
      userdel -r "$1" >/dev/null 2>&1 || userdel "$1" >/dev/null 2>&1 || true
    else
      deluser --remove-home "$1" >/dev/null 2>&1 || deluser "$1" >/dev/null 2>&1 || true
    fi
  ' hlab-unenroll "$HLAB_USER" </dev/null >/dev/null 2>&1 &
  echo '__M_ACCOUNT_REMOVING__'
fi

echo '__M_REVOKED__'
`

// validKeyBlob is the base64 alphabet. The blob is interpolated into a root
// shell script, so it is checked rather than trusted.
var validKeyBlob = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

// BuildRevokeScript renders the revocation script for one machine.
func BuildRevokeScript(opts RevokeOptions) (string, error) {
	if err := ValidateUsername(opts.User); err != nil {
		return "", err
	}
	if opts.KeyBlob != "" && !validKeyBlob.MatchString(opts.KeyBlob) {
		return "", fmt.Errorf("management key is not a base64 public key")
	}
	remove := "0"
	if opts.RemoveAccount {
		remove = "1"
	}

	return strings.NewReplacer(
		"__USER__", shellword.Quote(opts.User),
		"__BLOB__", shellword.Quote(opts.KeyBlob),
		"__REMOVE__", remove,
		"__COMMENT__", shellword.Quote(AccountComment),
		"__SUDOERS__", SudoersFragment,
		"__M_KEY_REMOVED__", MarkerKeyRemoved,
		"__M_KEY_ABSENT__", MarkerKeyAbsent,
		"__M_SUDO_REMOVED__", MarkerSudoRemoved,
		"__M_SUDO_ABSENT__", MarkerSudoAbsent,
		"__M_ACCOUNT_REMOVING__", MarkerAccountRemoving,
		"__M_REVOKED__", MarkerRevoked,
	).Replace(revokeTemplate), nil
}

// Revocation is what the script reported doing.
type Revocation struct {
	// KeyFiles are the authorized_keys files hlab's key was removed from.
	KeyFiles        []string
	SudoRemoved     bool
	AccountRemoving bool
	// Complete is false when the script stopped before its final marker.
	Complete bool
}

// ParseRevocation reads the script's markers.
func ParseRevocation(stdout string) Revocation {
	var r Revocation
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, MarkerKeyRemoved+" "):
			r.KeyFiles = append(r.KeyFiles, strings.TrimPrefix(line, MarkerKeyRemoved+" "))
		case line == MarkerSudoRemoved:
			r.SudoRemoved = true
		case line == MarkerAccountRemoving:
			r.AccountRemoving = true
		case line == MarkerRevoked:
			r.Complete = true
		}
	}
	return r
}

// KeyBlob extracts the base64 body of an authorized_keys line. It is parsed
// as a key rather than split on spaces, so only a real public key yields one.
func KeyBlob(authorizedKey string) (string, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return "", fmt.Errorf("not an authorized_keys entry: %w", err)
	}
	return base64.StdEncoding.EncodeToString(pub.Marshal()), nil
}

// LoadExistingKey reads hlab's management key without creating one. Revocation
// must never generate a key: a key made now cannot be on any machine. The
// error wraps os.ErrNotExist when there is no key.
func LoadExistingKey(path string) (*ManagementKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read management key %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("management key %s could not be read: %w", path, err)
	}
	return &ManagementKey{
		PrivatePath:   path,
		AuthorizedKey: authorizedKeyLine(signer.PublicKey(), ""),
		Fingerprint:   ssh.FingerprintSHA256(signer.PublicKey()),
	}, nil
}
