package health

import (
	"strings"
	"testing"
)

const sample = `##HLAB#disk#
/ 16 46G
/boot/firmware 14 438M
##HLAB#units#
NetworkManager-wait-online.service
##HLAB#containers#
caddy|running|-|0
homepage|running|healthy|1
technitium|running|unhealthy|0
oldthing|exited|-|0
flappy|running|-|9
##HLAB#stacks#
caddy 1 1
printfarm 3 2
##HLAB#reboot#
no
##HLAB#clock#
yes
##HLAB#uptime#
86400.5
##HLAB#end#
`

func TestParseFindsWhatIsWrong(t *testing.T) {
	r := Parse("apps-1", sample, DefaultThresholds())

	want := map[string]bool{
		"units":      true,
		"containers": true,
		"stacks":     true,
	}
	for _, f := range r.Findings {
		delete(want, f.Check)
	}
	if len(want) > 0 {
		t.Errorf("missed findings for %v; got %+v", want, r.Findings)
	}
	if r.Healthy() {
		t.Error("Healthy() is true with a failed unit and a dead container")
	}
	if r.Worst() != Fail {
		t.Errorf("Worst() = %q, want fail", r.Worst())
	}
}

// A container reporting itself unhealthy is a stronger signal than anything
// inferred from outside it, and "Up" is not health — the failure that started
// all this ran for two days in exactly that state.
func TestUnhealthyAndCrashLoopingContainersAreCaught(t *testing.T) {
	r := Parse("apps-1", sample, DefaultThresholds())

	var details []string
	for _, f := range r.Findings {
		details = append(details, f.Detail)
	}
	joined := strings.Join(details, "; ")

	for _, want := range []string{"technitium", "oldthing", "flappy"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no finding mentions %q: %s", want, joined)
		}
	}
	// A healthy container must not be reported.
	if strings.Contains(joined, "caddy") {
		t.Errorf("a healthy container was reported: %s", joined)
	}
}

// A stack missing a service is invisible to `docker ps`, because the thing to
// notice is the one that is absent.
func TestAStackShortOfServicesIsAFailure(t *testing.T) {
	r := Parse("apps-1", sample, DefaultThresholds())
	for _, f := range r.Findings {
		if f.Check == "stacks" {
			if f.Severity != Fail || !strings.Contains(f.Detail, "printfarm") {
				t.Errorf("stack finding = %+v", f)
			}
			return
		}
	}
	t.Error("a stack running 2 of 3 services was not reported")
}

// Thresholds are quiet on purpose: a check that cries wolf gets ignored, and
// an ignored check is worse than no check.
func TestDiskIsQuietUntilItMatters(t *testing.T) {
	if r := Parse("h", sample, DefaultThresholds()); hasCheck(r, "disk") {
		t.Error("a 16% filesystem was reported")
	}

	full := strings.Replace(sample, "/ 16 46G", "/ 88 4G", 1)
	r := Parse("h", full, DefaultThresholds())
	if !hasCheck(r, "disk") {
		t.Error("an 88% filesystem was not reported")
	}
	for _, f := range r.Findings {
		if f.Check == "disk" && f.Severity != Warn {
			t.Errorf("88%% should warn, not %q", f.Severity)
		}
	}

	critical := strings.Replace(sample, "/ 16 46G", "/ 97 200M", 1)
	for _, f := range Parse("h", critical, DefaultThresholds()).Findings {
		if f.Check == "disk" && f.Severity != Fail {
			t.Errorf("97%% should fail, not %q", f.Severity)
		}
	}
}

func TestCleanMachineIsHealthy(t *testing.T) {
	clean := "##HLAB#disk#\n/ 12 40G\n##HLAB#units#\n##HLAB#containers#\ncaddy|running|healthy|0\n" +
		"##HLAB#stacks#\ncaddy 1 1\n##HLAB#reboot#\nno\n##HLAB#clock#\nyes\n##HLAB#uptime#\n99999\n##HLAB#end#\n"

	r := Parse("apps-1", clean, DefaultThresholds())
	if !r.Healthy() {
		t.Errorf("a clean machine reported problems: %+v", r.Findings)
	}
}

// Truncated output must not read as a healthy machine — everything else here
// is lenient, so this is the only signal that the picture cannot be trusted.
func TestTruncatedOutputIsNotHealth(t *testing.T) {
	cut := strings.TrimSuffix(sample, "##HLAB#end#\n")
	r := Parse("apps-1", cut, DefaultThresholds())
	if !r.Partial {
		t.Error("truncated output was not flagged")
	}
	if r.Healthy() {
		t.Error("a truncated check reported as healthy")
	}
}

// An unsynchronised clock breaks TLS in ways that look like unrelated faults.
func TestClockDriftIsReported(t *testing.T) {
	drifted := strings.Replace(sample, "##HLAB#clock#\nyes", "##HLAB#clock#\nno", 1)
	if !hasCheck(Parse("h", drifted, DefaultThresholds()), "clock") {
		t.Error("an unsynchronised clock was not reported")
	}
}

func hasCheck(r Report, name string) bool {
	for _, f := range r.Findings {
		if f.Check == name {
			return true
		}
	}
	return false
}
