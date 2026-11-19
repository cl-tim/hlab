package migrate

import "github.com/cl-tim/hlab/internal/printerlab/inventory"

// klippeeFixedAccountArgs binds a fixed Klippee operation to the inventory's
// printer account with the same minimal environment used by the reviewed
// firmware helpers. Callers provide only compile-time command arguments.
func klippeeFixedAccountArgs(printer inventory.Printer, command ...string) []string {
	account := printer.User
	if printer.PrinterUser != "" {
		account = printer.PrinterUser
	}
	home := "/home/" + account
	executable := home + "/.local/bin/klippee"
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
	return append(environment, append([]string{executable}, command...)...)
}
