package updates

import (
	"strings"
	"testing"
)

// The upgrade must never remove a package to satisfy a dependency: that is the
// difference between a routine update and a machine that loses its network
// stack while nobody is looking at it.
func TestUpgradeNeverRemovesPackages(t *testing.T) {
	script := BuildUpgradeScript(false)

	if strings.Contains(script, "full-upgrade") || strings.Contains(script, "dist-upgrade") {
		t.Error("the upgrade uses a mode that can remove packages")
	}
	if !strings.Contains(script, "apt-get -y") || !strings.Contains(script, "\n  upgrade\n") {
		t.Error("no plain upgrade invocation found")
	}
	if strings.Contains(script, "--purge") || strings.Contains(script, "autoremove") {
		t.Error("the upgrade removes packages")
	}
}

// Without these dpkg stops on a changed config file and waits for an answer
// from an operator who is not there, and the run hangs until it is killed.
func TestUpgradeIsNonInteractive(t *testing.T) {
	script := BuildUpgradeScript(false)
	for _, want := range []string{
		"DEBIAN_FRONTEND=noninteractive",
		"--force-confold",
		"--force-confdef",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("upgrade script is missing %q", want)
		}
	}
}

// Nothing here may reboot a machine. A pending reboot is reported so the
// operator picks the moment.
func TestNothingReboots(t *testing.T) {
	for name, script := range map[string]string{
		"check":   CheckScript,
		"upgrade": BuildUpgradeScript(false),
		"images":  BuildImageScript(),
	} {
		for _, forbidden := range []string{"reboot -", "shutdown", "systemctl reboot", "init 6"} {
			if strings.Contains(script, forbidden) {
				t.Errorf("%s script contains %q", name, forbidden)
			}
		}
	}
}

// The check is a read. It refreshes the package index, which writes only to
// the cache, and must not install or start anything.
func TestCheckScriptOnlyReads(t *testing.T) {
	for _, forbidden := range []string{
		"apt-get -y", "apt-get install", "docker compose up", "docker compose pull",
		"docker rm", "docker stop", "systemctl restart",
	} {
		if strings.Contains(CheckScript, forbidden) {
			t.Errorf("the check script contains %q, which changes the machine", forbidden)
		}
	}
	if !strings.Contains(CheckScript, "apt-get -s upgrade") {
		t.Error("the check does not simulate the upgrade")
	}
}

// Every external tool is probed, so a machine without apt or without docker
// reports fewer sections instead of failing the whole check.
func TestCheckScriptProbesBeforeUse(t *testing.T) {
	for _, tool := range []string{"apt-get", "docker"} {
		if !strings.Contains(CheckScript, "command -v "+tool) {
			t.Errorf("the check uses %s without probing for it", tool)
		}
	}
}

// Only hlab's own stacks are refreshed. A hand-made project elsewhere has no
// reviewed compose file here, so pulling and recreating it would act on a
// definition nobody has read.
func TestImageRefreshTouchesOnlyManagedStacks(t *testing.T) {
	script := BuildImageScript()
	if !strings.Contains(script, "/opt/hlab/stacks/*/") {
		t.Error("the image refresh does not scope itself to hlab's stacks")
	}
	if !strings.Contains(script, "compose.yaml") {
		t.Error("the image refresh does not require a compose file to be present")
	}
	// pull must come before up, or the recreate has nothing new to use.
	if strings.Index(script, "compose pull") > strings.Index(script, "compose up") {
		t.Error("the images are pulled after the containers are recreated")
	}
}
