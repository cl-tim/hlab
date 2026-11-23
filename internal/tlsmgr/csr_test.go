package tlsmgr

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testCSR(t *testing.T, names []string, extra []pkix.Extension) []byte {
	t.Helper()
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "Test appliance"}, DNSNames: names, ExtraExtensions: extra}, k)
	if e != nil {
		t.Fatal(e)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: raw})
}

func TestCSRPolicyRequiresExplicitIdentitiesAndSignature(t *testing.T) {
	c := testConfig()
	c.ExternalNames = []string{"nas.example.test"}
	for _, names := range [][]string{nil, {"other.example.test"}, {"nas.example.test", "other.example.test"}, {"*.example.test"}} {
		if _, e := c.CheckCSR(testCSR(t, names, nil)); e == nil {
			t.Fatalf("accepted identities %v", names)
		}
	}
	csr := testCSR(t, []string{"nas.example.test"}, nil)
	if _, e := c.CheckCSR(csr); e != nil {
		t.Fatal(e)
	}
	p, _ := pem.Decode(csr)
	p.Bytes[len(p.Bytes)-1] ^= 1
	if _, e := c.CheckCSR(pem.EncodeToMemory(p)); e == nil {
		t.Fatal("accepted tampered CSR signature")
	}
	for _, days := range []int{0, 91} {
		if _, e := c.SignScript(csr, days); e == nil {
			t.Fatal("accepted invalid validity")
		}
	}
}

// This runs the actual signing shell against disposable keys only. No SSH,
// network, installed CA, system trust or real identity is involved.
func TestApplianceSigningReturnsConstrainedChainAndRotatesIssuer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell integration is run on Linux CI")
	}
	for _, tool := range []string{"openssl", "flock", "sh"} {
		if _, e := exec.LookPath(tool); e != nil {
			t.Skip(tool + " unavailable")
		}
	}
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "root")
	if e := os.Mkdir(rootDir, 0700); e != nil {
		t.Fatal(e)
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	root := &x509.Certificate{SerialNumber: big.NewInt(1234), Subject: pkix.Name{CommonName: "Disposable Test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(500 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, e := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, e := x509.MarshalECPrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(rootDir, "root.crt"), rootPEM, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(rootDir, "root.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); e != nil {
		t.Fatal(e)
	}
	constraints, _ := asn1.Marshal(struct{ CA bool }{true})
	csr := testCSR(t, []string{"nas.example.test"}, []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 19}, Critical: true, Value: constraints}})
	c := testConfig()
	c.ExternalNames = []string{"nas.example.test"}
	request, e := c.CheckCSR(csr)
	if e != nil {
		t.Fatal(e)
	}
	issuerDir := filepath.Join(dir, "appliances")
	run := func(days int) []byte {
		t.Helper()
		script, e := c.signScript(csr, days, rootDir, issuerDir)
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		out, e := cmd.Output()
		if e != nil {
			t.Fatalf("signing failed: %v", e)
		}
		if e = VerifyIssued(out, request, rootPEM); e != nil {
			t.Fatal(e)
		}
		if e = VerifyIssued(out, request, testRoot(t, true, now.Add(time.Hour))); e == nil {
			t.Fatal("accepted wrong root")
		}
		block, _ := pem.Decode(out)
		cert, e := x509.ParseCertificate(block.Bytes)
		if e != nil {
			t.Fatal(e)
		}
		if cert.IsCA || len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
			t.Fatal("CSR CA powers escaped into certificate")
		}
		return out
	}
	run(90)
	first, e := os.Readlink(filepath.Join(issuerDir, "current"))
	if e != nil {
		t.Fatal(e)
	}
	run(90)
	again, _ := os.Readlink(filepath.Join(issuerDir, "current"))
	if first != again {
		t.Fatal("healthy intermediate rotated unnecessarily")
	}
	if e = os.Remove(filepath.Join(issuerDir, "current", "intermediate.crt")); e != nil {
		t.Fatal(e)
	}
	run(90)
	rotated, _ := os.Readlink(filepath.Join(issuerDir, "current"))
	if first == rotated {
		t.Fatal("missing issuer was not replaced")
	}
}
