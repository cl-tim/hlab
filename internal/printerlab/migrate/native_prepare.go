package migrate

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

var regexpAPIKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// prepareBuild creates only missing repositories. Existing repositories must
// already match the pinned commit and be clean; it never resets local work.
func (b *nativeBackend) prepareBuild() error {
	for _, key := range []string{"klipper-ref", "katapult-ref"} {
		if !commitPattern.MatchString(b.value(key)) {
			return fmt.Errorf("--%s must be a full pinned commit", key)
		}
	}
	for _, key := range []string{"klipper-dir", "katapult-dir"} {
		if _, err := b.required(key); err != nil {
			return err
		}
	}
	script, err := b.helper("prepare-build.sh")
	if err != nil {
		return err
	}
	args := []string{"-s", "--", b.value("klipper-dir"), b.value("katapult-dir"), b.value("klipper-ref"), b.value("katapult-ref")}
	if distro := b.value("wsl-distro"); distro != "" {
		if strings.HasPrefix(distro, "-") || strings.ContainsAny(distro, "\x00\r\n") {
			return fmt.Errorf("invalid WSL distribution")
		}
		return b.run("wsl", append([]string{"-d", distro, "--", "bash"}, args...), script)
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("Windows firmware builds require a WSL distribution")
	}
	return b.run("bash", args, script)
}
