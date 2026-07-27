// Package secrets manages the credential files a stack reads at runtime.
//
// The pattern it formalises was arrived at by hand three times: a value is
// generated on the machine that will use it, written beside the stack at 0600,
// and referenced from the compose file rather than embedded in it. Compose
// files are reviewed, committed and shared; the things they need to
// authenticate with must not be.
package secrets

import (
	"fmt"
	"strings"
)

// StackDir is where a stack's files live on a managed machine.
const StackDir = "/opt/hlab/stacks"

// Markers the scripts print.
const (
	MarkerWritten = "hlab:secret-written"
	MarkerMissing = "hlab:secret-missing"
)

// DefaultLength is how many characters a generated secret gets.
//
// 32 of this alphabet is a little under 190 bits, which is far past anything
// that matters here and still short enough to retype off a screen if something
// has gone badly wrong.
const DefaultLength = 32

// generateTemplate creates a random value on the machine that will use it.
//
// Generated there rather than here, on purpose: a secret that is never
// transmitted cannot be intercepted, logged by an intermediate shell, or left
// in the operator's terminal scrollback. It is also why there is no way to ask
// for a value back except by asking explicitly.
const generateTemplate = `
set -eu

SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

DIR=__DIR__
FILE=__FILE__

$SUDO mkdir -p "$DIR"

if [ -s "$FILE" ] && [ __FORCE__ = 0 ]; then
  echo 'hlab:error a secret of that name already exists; pass --force to replace it' >&2
  exit 70
fi

VALUE=$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c __LENGTH__)
__WRITE__

$SUDO chmod 0600 "$FILE"
echo '__M_WRITTEN__'
`

// setTemplate writes a value supplied by the operator.
//
// The value arrives on stdin rather than in the command line, so it never
// appears in the machine's process list where any other user could read it.
const setTemplate = `
set -eu

SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

DIR=__DIR__
FILE=__FILE__

$SUDO mkdir -p "$DIR"
VALUE=$(cat)
__WRITE__

$SUDO chmod 0600 "$FILE"
echo '__M_WRITTEN__'
`

// rawWrite puts the value in a file of its own.
const rawWrite = `printf '%s' "$VALUE" | $SUDO tee "$FILE" >/dev/null`

// envWrite adds or replaces one KEY=value line in an env file, leaving the
// rest of the file alone — a stack usually needs more than one.
const envWrite = `
TMP=$($SUDO mktemp)
if [ -f "$FILE" ]; then
  $SUDO grep -v '^__KEY__=' "$FILE" > "$TMP" 2>/dev/null || true
fi
printf '%s=%s\n' __KEY__ "$VALUE" | $SUDO tee -a "$TMP" >/dev/null
$SUDO mv "$TMP" "$FILE"
`

// Options describe one secret.
type Options struct {
	// Stack is the stack directory the secret belongs to.
	Stack string
	// File is the file's name inside that directory.
	File string
	// Key, when set, makes this one entry in an env file rather than a file
	// whose whole content is the secret.
	Key string
	// Length is how many characters to generate.
	Length int
	// Force replaces an existing secret.
	Force bool
}

// BuildGenerateScript renders the script that creates a secret on the machine.
func BuildGenerateScript(o Options) (string, error) {
	write, err := o.writeStep()
	if err != nil {
		return "", err
	}
	length := o.Length
	if length <= 0 {
		length = DefaultLength
	}
	if length < 8 || length > 512 {
		return "", fmt.Errorf("secret length %d is outside the sensible range of 8 to 512", length)
	}

	force := "0"
	if o.Force || o.Key != "" {
		// An env file legitimately holds several keys, so writing another one
		// is not a collision the way replacing a whole file would be.
		force = "1"
	}

	return strings.NewReplacer(
		"__DIR__", shellQuote(StackDir+"/"+o.Stack),
		"__FILE__", shellQuote(StackDir+"/"+o.Stack+"/"+o.File),
		"__LENGTH__", fmt.Sprint(length),
		"__FORCE__", force,
		"__WRITE__", write,
		"__M_WRITTEN__", MarkerWritten,
	).Replace(generateTemplate), nil
}

// BuildSetScript renders the script that stores a value read from stdin.
func BuildSetScript(o Options) (string, error) {
	write, err := o.writeStep()
	if err != nil {
		return "", err
	}
	return strings.NewReplacer(
		"__DIR__", shellQuote(StackDir+"/"+o.Stack),
		"__FILE__", shellQuote(StackDir+"/"+o.Stack+"/"+o.File),
		"__WRITE__", write,
		"__M_WRITTEN__", MarkerWritten,
	).Replace(setTemplate), nil
}

// writeStep picks between a whole-file secret and one key in an env file.
func (o Options) writeStep() (string, error) {
	if err := ValidateName(o.Stack); err != nil {
		return "", fmt.Errorf("stack %q: %w", o.Stack, err)
	}
	if err := ValidateFile(o.File); err != nil {
		return "", err
	}
	if o.Key == "" {
		return rawWrite, nil
	}
	if err := ValidateKey(o.Key); err != nil {
		return "", err
	}
	return strings.ReplaceAll(envWrite, "__KEY__", shellQuote(o.Key)), nil
}

// ListScript reports the secrets a machine holds, and never their values.
//
// The whole walk runs under sudo: stack directories are 0750 and root-owned,
// so an unprivileged shell cannot expand a glob inside them and would report a
// machine full of credentials as having none.
//
// What counts as a secret is deliberately narrow. Listing every file beside a
// stack drowns the answer — Technitium alone keeps hundreds of blocklist and
// statistics files there. A file qualifies if its mode says somebody
// restricted it, or if its name says what it holds. The two together catch a
// credential that was *meant* to be private but is not, which is the case
// worth shouting about.
const ListScript = `
SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

$SUDO find /opt/hlab/stacks -mindepth 2 -maxdepth 4 -type f 2>/dev/null | while read -r f; do
  base=${f##*/}
  case "$base" in
    compose.yaml|*.yml|*.yaml|Caddyfile) continue ;;
  esac

  mode=$($SUDO stat -c %a "$f" 2>/dev/null)
  keep=0
  case "$mode" in
    600|400) keep=1 ;;
  esac
  case "$base" in
    *.env|*password*|*secret*|*token*|*credential*|*.key|*.pem) keep=1 ;;
  esac
  [ "$keep" = 1 ] || continue

  rel=${f#/opt/hlab/stacks/}
  printf '%s|%s|%s|%s\n'     "${rel%%/*}" "${rel#*/}" "$mode" "$($SUDO wc -c "$f" 2>/dev/null | awk '{print $1}')"
done
`

// BuildShowScript renders the script that prints one secret's value.
func BuildShowScript(stack, file string) (string, error) {
	if err := ValidateName(stack); err != nil {
		return "", fmt.Errorf("stack %q: %w", stack, err)
	}
	if err := ValidateFile(file); err != nil {
		return "", err
	}

	return `
SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi
FILE=` + shellQuote(StackDir+"/"+stack+"/"+file) + `
if [ ! -f "$FILE" ]; then
  echo '` + MarkerMissing + `'
  exit 0
fi
$SUDO cat "$FILE"
`, nil
}

// ValidateName rejects a stack name that would escape the stack directory.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("a stack name is required")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("invalid stack name %q", name)
		}
	}
	return nil
}

// ValidateFile rejects anything that would write outside the stack directory.
//
// The name becomes a path in a script running as root, so this is a security
// control rather than tidiness. One level of "config/" is allowed because that
// is where several images expect to read from.
func ValidateFile(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("a secret needs a file name")
	case strings.Contains(name, ".."):
		return fmt.Errorf("invalid secret file %q", name)
	case strings.HasPrefix(name, "/"), strings.HasPrefix(name, "-"):
		return fmt.Errorf("invalid secret file %q", name)
	case strings.ContainsAny(name, "\\\x00\n'\""):
		return fmt.Errorf("invalid secret file %q", name)
	}

	if n := strings.Count(name, "/"); n > 1 {
		return fmt.Errorf("invalid secret file %q: at most one directory is allowed", name)
	} else if n == 1 && !strings.HasPrefix(name, "config/") {
		return fmt.Errorf("invalid secret file %q: only config/ may be used", name)
	}
	return nil
}

// ValidateKey rejects env keys that are not env keys.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("an env key is required")
	}
	for i, r := range key {
		switch {
		case r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return fmt.Errorf("invalid env key %q: use A-Z, digits and underscore", key)
		}
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
