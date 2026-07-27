package updates

import (
	"strings"
	"testing"
)

const sample = `##HLAB#manager#
apt
##HLAB#refresh#
ok
##HLAB#packages#
curl 8.5.0-2ubuntu10.6
libssl3 3.0.13-0ubuntu3.4
zlib1g 1:1.3.dfsg-3.1ubuntu2.1
##HLAB#upgradable#
curl noble-updates
libssl3 noble-security
zlib1g noble-updates
docker-ce noble/stable
##HLAB#reboot#
required
linux-image-6.8.0-51-generic
libc6
##HLAB#kernel#
6.8.0-49-generic
##HLAB#containers#
caddy caddy:2-alpine current
homepage ghcr.io/gethomepage/homepage:latest stale
##HLAB#stacks#
caddy
homepage
##HLAB#end#
`

func TestParseReadsEachSection(t *testing.T) {
	s := Parse("apps-1", sample)

	if s.Manager != "apt" || s.Kernel != "6.8.0-49-generic" {
		t.Errorf("manager/kernel = %q/%q", s.Manager, s.Kernel)
	}
	if len(s.Packages) != 3 {
		t.Fatalf("got %d packages, want 3: %+v", len(s.Packages), s.Packages)
	}
	if s.Packages[0].Name != "curl" || s.Packages[0].Version != "8.5.0-2ubuntu10.6" {
		t.Errorf("package[0] = %+v", s.Packages[0])
	}
	if s.Partial {
		t.Error("Partial set even though the end marker is present")
	}
	if s.IndexStale {
		t.Error("IndexStale set even though the refresh reported ok")
	}
}

// A security update is the one an operator acts on first, so it has to be
// distinguishable — and the archive is only named in the upgradable listing,
// not in what apt says it would install.
func TestParseMarksSecurityUpdates(t *testing.T) {
	s := Parse("apps-1", sample)

	if got := s.SecurityCount(); got != 1 {
		t.Errorf("SecurityCount = %d, want 1", got)
	}
	for _, p := range s.Packages {
		if p.Name == "libssl3" && !p.Security {
			t.Error("libssl3 comes from noble-security but was not marked")
		}
		if p.Name == "curl" && p.Security {
			t.Error("curl comes from noble-updates and must not be marked security")
		}
	}
}

// A package apt lists as upgradable but declines to install is held back —
// usually because upgrading it would remove something. Reporting it as simply
// "up to date" would be a lie of omission.
func TestParseReportsHeldBackPackages(t *testing.T) {
	s := Parse("apps-1", sample)

	if len(s.HeldBack) != 1 || s.HeldBack[0] != "docker-ce" {
		t.Errorf("HeldBack = %v, want [docker-ce]", s.HeldBack)
	}
}

func TestParseReadsRebootAndContainers(t *testing.T) {
	s := Parse("apps-1", sample)

	if !s.Reboot {
		t.Fatal("reboot not detected")
	}
	if len(s.RebootPkgs) != 2 {
		t.Errorf("RebootPkgs = %v", s.RebootPkgs)
	}

	stale := s.StaleContainers()
	if len(stale) != 1 || stale[0].Name != "homepage" {
		t.Errorf("StaleContainers = %+v, want just homepage", stale)
	}
	if s.UpToDate() {
		t.Error("UpToDate with packages, a stale container and a pending reboot")
	}
}

// Truncated output must not read as a clean machine. Everything else here is
// lenient, so this is the one signal that the picture cannot be trusted.
func TestParseFlagsTruncatedOutput(t *testing.T) {
	cut := strings.TrimSuffix(sample, "##HLAB#end#\n")
	if s := Parse("apps-1", cut); !s.Partial {
		t.Error("truncated output was not flagged as partial")
	}
}

// A machine with nothing pending is the common case and must come out clean
// rather than as an empty-but-suspicious result.
func TestParseCleanMachine(t *testing.T) {
	s := Parse("apps-1", "##HLAB#manager#\napt\n##HLAB#refresh#\nok\n##HLAB#reboot#\nno\n##HLAB#end#\n")
	if !s.UpToDate() {
		t.Errorf("UpToDate = false for a machine with nothing pending: %+v", s)
	}
	if s.Partial || s.IndexStale {
		t.Errorf("Partial/IndexStale set wrongly: %+v", s)
	}
}

// If the index could not be refreshed the counts describe whatever the machine
// last knew, which is worth saying rather than presenting as current.
func TestParseNotesAStaleIndex(t *testing.T) {
	s := Parse("apps-1", "##HLAB#manager#\napt\n##HLAB#refresh#\nfailed\n##HLAB#end#\n")
	if !s.IndexStale {
		t.Error("a failed index refresh was not reported")
	}
}
