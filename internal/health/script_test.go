package health

import (
	"strings"
	"testing"
)

// The check must not change the machine it is judging.
func TestCheckScriptOnlyReads(t *testing.T) {
	for _, forbidden := range []string{
		"docker compose up", "docker compose down", "docker restart", "docker rm",
		"systemctl start", "systemctl restart", "systemctl stop",
		"apt-get", "rm -",
		// Not the bare word: the check reads /var/run/reboot-required, which is
		// the opposite of rebooting.
		"systemctl reboot", "shutdown", "/sbin/reboot",
	} {
		if strings.Contains(CheckScript, forbidden) {
			t.Errorf("the check script contains %q, which changes the machine", forbidden)
		}
	}
}

// Every external tool is probed, so a machine without docker or without
// systemd reports fewer sections rather than failing the whole check.
func TestCheckScriptProbesBeforeUse(t *testing.T) {
	for _, tool := range []string{"systemctl", "docker", "timedatectl"} {
		if !strings.Contains(CheckScript, "command -v "+tool) {
			t.Errorf("the check uses %s without probing for it", tool)
		}
	}
}

// Filesystems that are not the machine's own would make every host mounting a
// full NAS report itself sick, and pseudo-filesystems fill and empty on their
// own. Either would make the check noise.
func TestDiskCheckExcludesWhatIsNotTheMachinesOwnDisk(t *testing.T) {
	for _, kind := range []string{"tmpfs", "overlay", "squashfs", "cifs", "nfs"} {
		if !strings.Contains(CheckScript, kind) {
			t.Errorf("the disk check does not exclude %s", kind)
		}
	}
}
