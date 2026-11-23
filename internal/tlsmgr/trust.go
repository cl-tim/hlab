package tlsmgr

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Root validates exactly one self-signed, currently valid CA certificate.
// Private keys, bundles and trailing garbage are refused before trust changes.
func Root(raw []byte) (*x509.Certificate, string, error) {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("-----BEGIN CERTIFICATE-----")) {
		return nil, "", fmt.Errorf("expected exactly one PEM root certificate")
	}
	p, rest := pem.Decode(raw)
	if p == nil || p.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, "", fmt.Errorf("expected exactly one PEM root certificate")
	}
	c, err := x509.ParseCertificate(p.Bytes)
	if err != nil {
		return nil, "", err
	}
	if !c.IsCA || !c.BasicConstraintsValid || c.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, "", fmt.Errorf("certificate is not a CA")
	}
	if err = c.CheckSignatureFrom(c); err != nil {
		return nil, "", fmt.Errorf("CA is not self-signed: %w", err)
	}
	now := time.Now()
	if now.Before(c.NotBefore) || !now.Before(c.NotAfter) {
		return nil, "", fmt.Errorf("CA is not currently valid")
	}
	h := sha256.Sum256(c.Raw)
	return c, hex.EncodeToString(h[:]), nil
}

// Pin is trust on first use over an already authenticated SSH connection. It
// never replaces an existing anchor, even when --apply is used again.
func Pin(path string, raw []byte) (string, error) {
	_, fp, err := Root(raw)
	if err != nil {
		return "", err
	}
	if existing, err := os.ReadFile(path); err == nil {
		_, old, e := Root(existing)
		if e != nil {
			return "", fmt.Errorf("read pinned root: %w", e)
		}
		if old != fp {
			return "", fmt.Errorf("CA changed: pinned %s, remote %s; refusing automatic root rotation", old, fp)
		}
		return fp, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return fp, err
}

func TrustScript(raw []byte) (string, error) {
	_, fp, err := Root(raw)
	if err != nil {
		return "", err
	}
	return `set -eu
if command -v update-ca-certificates >/dev/null 2>&1; then
  anchors=/usr/local/share/ca-certificates
  refresh=update-ca-certificates
elif command -v update-ca-trust >/dev/null 2>&1; then
  anchors=/etc/pki/ca-trust/source/anchors
  refresh='update-ca-trust extract'
else
  echo 'No supported system CA trust store (need update-ca-certificates or update-ca-trust)' >&2
  exit 1
fi
command -v openssl >/dev/null
install -d -m 755 "$anchors"
anchor="$anchors/hlab-` + fp + `.crt"
stage=$(mktemp)
trap 'rm -f "$stage"' EXIT
cat >"$stage" <<'HLAB_CA_ROOT'
` + string(raw) + `
HLAB_CA_ROOT
if [ -e "$anchor" ] && ! cmp -s "$stage" "$anchor"; then
  echo 'Existing hlab trust anchor has unexpected contents' >&2; exit 1
fi
if ! test -f "$anchor"; then
  install -m 644 "$stage" "$anchor"
fi
# Always refresh: a previous run may have written the anchor and failed here.
$refresh >/dev/null
openssl verify "$anchor" >/dev/null
printf 'Trusted SHA256 ` + fp + `\n'
`, nil
}
