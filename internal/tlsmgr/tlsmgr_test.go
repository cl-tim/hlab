package tlsmgr

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRoot(t *testing.T, ca bool, until time.Time) []byte {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	c := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: until, IsCA: ca, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, e := x509.CreateCertificate(rand.Reader, c, c, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestPinRefusesAuthorityChangesAndDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pki", "root.crt")
	first := testRoot(t, true, time.Now().Add(time.Hour))
	fp, e := Pin(path, first)
	if e != nil {
		t.Fatal(e)
	}
	again, e := Pin(path, first)
	if e != nil || fp != again {
		t.Fatalf("repeat pin: %q %v", again, e)
	}
	second := testRoot(t, true, time.Now().Add(time.Hour))
	if _, e = Pin(path, second); e == nil || !strings.Contains(e.Error(), "CA changed") {
		t.Fatalf("rotation accepted: %v", e)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, first) {
		t.Fatal("changed pinned CA")
	}
}

func TestRootRejectsAnythingOtherThanOneValidRoot(t *testing.T) {
	good := testRoot(t, true, time.Now().Add(time.Hour))
	for name, raw := range map[string][]byte{"leaf": testRoot(t, false, time.Now().Add(time.Hour)), "expired": testRoot(t, true, time.Now().Add(-time.Minute)), "bundle": append(append([]byte{}, good...), good...), "trailing": append(append([]byte{}, good...), []byte("garbage")...), "key": []byte("-----BEGIN PRIVATE KEY-----\nYQo=\n-----END PRIVATE KEY-----\n")} {
		t.Run(name, func(t *testing.T) {
			if _, _, e := Root(raw); e == nil {
				t.Fatal("invalid trust anchor accepted")
			}
		})
	}
}

func testConfig() Config {
	return Config{Authority: "pi", AuthorityDNS: "pi.lan", Services: []Service{{Name: "printer", Target: "printer:voron", Names: []string{"voron.lan"}, Upstream: "http://127.0.0.1:80"}}}
}

func TestRenderingUsesOneCAAndDeviceLocalUpstreams(t *testing.T) {
	c := testConfig()
	authority, e := c.Render("host:pi")
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"ca hlab", "domains voron.lan", "challenges tls-alpn-01", "skip_install_trust", "auto_https disable_redirects"} {
		if !strings.Contains(authority, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(authority, "reverse_proxy") {
		t.Fatal("authority proxies another device")
	}
	leaf, e := c.Render("printer:voron")
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"https://voron.lan:443", "https://pi.lan:8443/acme/hlab/directory", "acme_ca_root /etc/hlab/tls/root.crt", "disable_http_challenge", "reverse_proxy http://127.0.0.1:80"} {
		if !strings.Contains(leaf, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(leaf, "issuer internal") || strings.Contains(leaf, "pki {") {
		t.Fatal("leaf is an independent authority")
	}
}

func TestConfigurationRefusesInjectionRemoteHTTPAndAmbiguousNames(t *testing.T) {
	for _, mutation := range []func(*Config){
		func(c *Config) { c.AuthorityDNS = "pi.lan\n}" }, func(c *Config) { c.Services[0].Names = []string{"*.lan"} },
		func(c *Config) { c.Services[0].Upstream = "http://other-device.lan:80" }, func(c *Config) { c.Services[0].Upstream = "http://user:pass@127.0.0.1:80" },
		func(c *Config) { c.Services[0].Upstream = "http://127.0.0.1:443" }, func(c *Config) { c.Services[0].Port = 80 },
		func(c *Config) {
			s := c.Services[0]
			s.Name = "duplicate"
			s.Target = "host:other"
			c.Services = append(c.Services, s)
		},
		func(c *Config) { c.Services[0].Upstream = "http://127.0.0.1:80/ { respond pwned }" },
	} {
		c := testConfig()
		mutation(&c)
		if e := c.Validate(); e == nil {
			t.Fatalf("invalid configuration accepted: %+v", c)
		}
	}
}

func TestTrustScriptOnlyInstallsPublicRoot(t *testing.T) {
	raw := testRoot(t, true, time.Now().Add(time.Hour))
	script, e := TrustScript(raw)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"update-ca-certificates", "update-ca-trust extract", "openssl verify", "Existing hlab trust anchor has unexpected contents"} {
		if !strings.Contains(script, s) {
			t.Error("missing " + s)
		}
	}
	if strings.Contains(script, "PRIVATE KEY") {
		t.Fatal("private key in trust script")
	}
	if _, e = TrustScript([]byte("not a cert")); e == nil {
		t.Fatal("invalid anchor generated a script")
	}
}

func TestBinaryExtractorDoesNotExtractPathsOrLinks(t *testing.T) {
	for _, typ := range []byte{tar.TypeSymlink, tar.TypeDir} {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(&tar.Header{Name: "caddy", Typeflag: typ, Linkname: "/etc/shadow"})
		_ = tw.Close()
		_ = gz.Close()
		if _, e := extractBinary(b.Bytes()); e == nil {
			t.Fatal("non-regular member accepted")
		}
	}
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../../caddy", Size: 4, Mode: 0755})
	_, _ = tw.Write([]byte("test"))
	_ = tw.Close()
	_ = gz.Close()
	if _, e := extractBinary(b.Bytes()); e == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestArchitectureAccountsFor32BitUserspace(t *testing.T) {
	a, e := Architecture("aarch64", "32")
	if e != nil || a != "armv7" {
		t.Fatalf("%s %v", a, e)
	}
	if _, e = Architecture("unknown", "64"); e == nil {
		t.Fatal("unknown accepted")
	}
}
