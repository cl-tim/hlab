package tlsmgr

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"
	"time"
)

// CheckCSR accepts only signed server requests for explicitly declared appliance
// identities. Only SANs are used; requested CA powers and other extensions are
// never copied to the issued certificate. The appliance keeps its private key.
func (c Config) CheckCSR(raw []byte) (*x509.CertificateRequest, error) {
	p, rest := pem.Decode(raw)
	if p == nil || (p.Type != "CERTIFICATE REQUEST" && p.Type != "NEW CERTIFICATE REQUEST") || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("expected one PEM certificate request")
	}
	r, err := x509.ParseCertificateRequest(p.Bytes)
	if err != nil {
		return nil, err
	}
	if err = r.CheckSignature(); err != nil {
		return nil, err
	}
	switch key := r.PublicKey.(type) {
	case *rsa.PublicKey:
		if key.N.BitLen() < 2048 {
			return nil, fmt.Errorf("RSA key must be at least 2048 bits")
		}
	case *ecdsa.PublicKey:
		if key.Curve.Params().BitSize < 256 {
			return nil, fmt.Errorf("EC key must be at least 256 bits")
		}
	case ed25519.PublicKey:
	default:
		return nil, fmt.Errorf("unsupported CSR key type")
	}
	if len(r.DNSNames)+len(r.IPAddresses) == 0 || len(r.EmailAddresses) > 0 || len(r.URIs) > 0 {
		return nil, fmt.Errorf("CSR must have DNS/IP SANs and no email/URI identities")
	}
	for _, n := range r.DNSNames {
		if !validDNS(n) || !slices.Contains(c.ExternalNames, n) {
			return nil, fmt.Errorf("CSR name %q is not listed in tls.externalNames", n)
		}
	}
	for _, ip := range r.IPAddresses {
		if !slices.Contains(c.ExternalNames, ip.String()) {
			return nil, fmt.Errorf("CSR IP %s is not listed in tls.externalNames", ip)
		}
	}
	return r, nil
}

func (c Config) SignScript(raw []byte, days int) (string, error) {
	return c.signScript(raw, days, "/var/lib/hlab-tls/caddy/pki/authorities/hlab", "/var/lib/hlab-tls/appliance-issuer")
}

func (c Config) signScript(raw []byte, days int, rootDir, issuerDir string) (string, error) {
	if days < 1 || days > 90 {
		return "", fmt.Errorf("certificate lifetime must be between 1 and 90 days")
	}
	r, err := c.CheckCSR(raw)
	if err != nil {
		return "", err
	}
	var san []string
	for _, n := range r.DNSNames {
		san = append(san, "DNS:"+n)
	}
	for _, ip := range r.IPAddresses {
		san = append(san, "IP:"+ip.String())
	}
	ext := "basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\nsubjectAltName=" + strings.Join(san, ",") + "\n"
	return fmt.Sprintf(`set -eu
umask 077
root=%s
base=%s
install -d -m 700 "$base"
exec 9>"$base/sign.lock"
flock -x 9
ca="$base/current"
# Appliance imports need longer lifetimes than Caddy's automatic 12-hour
# certificates. A separate path-length-zero issuer keeps those lifecycles apart.
if ! test -f "$ca/intermediate.crt" || ! openssl x509 -checkend %d -noout -in "$ca/intermediate.crt" >/dev/null; then
 openssl x509 -checkend 31536120 -noout -in "$root/root.crt" >/dev/null || {
  echo 'Root expires too soon to create an appliance issuer; plan root rotation' >&2; exit 1;
 }
 issuer=$(mktemp -d "$base/issuer.XXXXXXXX")
 openssl ecparam -name prime256v1 -genkey -noout -out "$issuer/intermediate.key"
 openssl req -new -key "$issuer/intermediate.key" -subj '/CN=hlab Appliance Issuer' -out "$issuer/intermediate.csr"
 printf 'basicConstraints=critical,CA:TRUE,pathlen:0\nkeyUsage=critical,keyCertSign,cRLSign\nsubjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid,issuer\n' >"$issuer/extensions"
 openssl x509 -req -in "$issuer/intermediate.csr" -CA "$root/root.crt" -CAkey "$root/root.key" -set_serial "0x$(openssl rand -hex 16)" -days 365 -sha256 -extfile "$issuer/extensions" -out "$issuer/intermediate.crt"
 openssl verify -CAfile "$root/root.crt" "$issuer/intermediate.crt" >/dev/null
 ln -sfn "$issuer" "$base/current.new"
 mv -Tf "$base/current.new" "$base/current"
fi
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
printf %%s %s | base64 -d >"$stage/request.csr"
printf %%s %s | base64 -d >"$stage/extensions"
openssl x509 -req -in "$stage/request.csr" -CA "$ca/intermediate.crt" -CAkey "$ca/intermediate.key" -set_serial "0x$(openssl rand -hex 16)" -days %d -sha256 -extfile "$stage/extensions" -out "$stage/leaf.crt"
cat "$stage/leaf.crt" "$ca/intermediate.crt"
`, quote(rootDir), quote(issuerDir), days*86400+60, quote(base64.StdEncoding.EncodeToString(raw)), quote(base64.StdEncoding.EncodeToString([]byte(ext))), days), nil
}

// VerifyIssued checks the returned chain and key binding before a certificate
// is exported. A remote command exiting zero is not sufficient evidence.
func VerifyIssued(raw []byte, csr *x509.CertificateRequest, root []byte) error {
	anchor, _, err := Root(root)
	if err != nil {
		return err
	}
	p, rest := pem.Decode(raw)
	if p == nil || p.Type != "CERTIFICATE" {
		return fmt.Errorf("missing issued certificate")
	}
	leaf, err := x509.ParseCertificate(p.Bytes)
	if err != nil {
		return err
	}
	if leaf.IsCA {
		return fmt.Errorf("issuer returned a CA certificate instead of a server certificate")
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || leaf.KeyUsage&x509.KeyUsageCertSign != 0 || len(leaf.EmailAddresses) > 0 || len(leaf.URIs) > 0 {
		return fmt.Errorf("issued certificate has unexpected powers or identities")
	}
	if leaf.NotAfter.After(time.Now().Add(90*24*time.Hour + 2*time.Minute)) {
		return fmt.Errorf("issued certificate exceeds maximum validity")
	}
	want, _ := x509.MarshalPKIXPublicKey(csr.PublicKey)
	got, _ := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if !bytes.Equal(want, got) {
		return fmt.Errorf("issued certificate does not match the CSR key")
	}
	if !slices.Equal(leaf.DNSNames, csr.DNSNames) || len(leaf.IPAddresses) != len(csr.IPAddresses) {
		return fmt.Errorf("issued certificate SANs do not match CSR")
	}
	for i, ip := range csr.IPAddresses {
		if !leaf.IPAddresses[i].Equal(ip) {
			return fmt.Errorf("issued certificate IP SAN mismatch")
		}
	}
	pool := x509.NewCertPool()
	pool.AddCert(anchor)
	intermediates := x509.NewCertPool()
	for len(bytes.TrimSpace(rest)) > 0 {
		p, rest = pem.Decode(rest)
		if p == nil || p.Type != "CERTIFICATE" {
			return fmt.Errorf("invalid issued chain")
		}
		c, err := x509.ParseCertificate(p.Bytes)
		if err != nil {
			return err
		}
		intermediates.AddCert(c)
	}
	_, err = leaf.Verify(x509.VerifyOptions{Roots: pool, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}
