package migrate

import (
	"strings"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

// klippeeFixedAccountArgs binds a fixed Klippee operation to the inventory's
// printer account with the same minimal environment used by the reviewed
// firmware helpers. Callers provide only compile-time command arguments.
func klippeeFixedAccountArgs(printer inventory.Printer, command ...string) []string {
	return klippeeFixedAccountDispatch(printer, command, "")
}

func klippeeFixedAccountDispatch(printer inventory.Printer, command []string, legacyOverride string) []string {
	account := printer.User
	if printer.PrinterUser != "" {
		account = printer.PrinterUser
	}
	home := "/home/" + account
	native := home + "/.local/bin/hlab"
	legacy := home + "/.local/bin/klippee"
	environment := []string{
		"/usr/bin/env", "-i",
		"HOME=" + home,
		"USER=" + account,
		"LOGNAME=" + account,
		"KLIPPEE_STATE_DIR=" + home + "/.klippee",
		"PATH=/usr/bin:/bin",
		"LANG=C",
		"LC_ALL=C",
	}
	if account != printer.User {
		environment = append([]string{"sudo", "-n", "-u", account, "--"}, environment...)
	}
	nativeArgs := command
	if len(command) == 2 && command[0] == "pull" && command[1] == "--force" {
		nativeArgs = []string{"apply", "--yes"}
	}
	shellArgs := func(args []string) string {
		quoted := make([]string, 0, len(args))
		for _, arg := range args {
			quoted = append(quoted, quote(arg))
		}
		return strings.Join(quoted, " ")
	}
	if legacyOverride == "" {
		legacyOverride = "exec " + shellArgs(append([]string{legacy}, command...))
	}
	script := "for hlab_bin in " + shellArgs([]string{native, "/usr/local/bin/hlab", "/usr/bin/hlab"}) +
		"; do if [ -x \"$hlab_bin\" ] && \"$hlab_bin\" klippee --help >/dev/null 2>&1; then exec \"$hlab_bin\" klippee " +
		shellArgs(nativeArgs) + "; fi; done; " + legacyOverride
	return append(environment, "/bin/sh", "-c", quote(script))
}
