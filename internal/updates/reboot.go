package updates

import (
	"strings"
	"time"
)

// Markers the reboot script prints.
const (
	MarkerRebooting = "hlab:rebooting"
	MarkerNoReboot  = "hlab:no-reboot-needed"
)

// RebootTimeout bounds how long a machine is given to come back.
//
// Generous on purpose: a Raspberry Pi on a slow card with a new kernel and a
// filesystem check can take minutes, and concluding it is gone when it is
// merely slow would send an operator to the cupboard for nothing.
const RebootTimeout = 6 * time.Minute

// BootIDScript reads the identifier the kernel generates once per boot.
//
// This is how a reboot is proved rather than assumed. A machine that answers
// SSH again may simply never have gone down — if the reboot silently failed,
// waiting for it to "come back" succeeds immediately and reports a reboot that
// did not happen. The boot id changes only when the kernel starts.
const BootIDScript = `cat /proc/sys/kernel/random/boot_id 2>/dev/null || true`

// rebootTemplate restarts a machine, but only if one is actually pending.
//
// The reboot is detached for the same reason network changes are: it takes
// down the connection this script is running over, and a shell killed by that
// SIGHUP can take the command with it before it has done anything. Detached,
// the script reports cleanly and the machine goes on its own.
const rebootTemplate = `
set -eu

SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

if [ ! -f /var/run/reboot-required ] && [ "$1" != force ]; then
  echo '__M_NO_REBOOT__'
  exit 0
fi

$SUDO setsid sh -c 'sleep 3; systemctl reboot >/dev/null 2>&1 || reboot' >/dev/null 2>&1 </dev/null &
echo '__M_REBOOTING__'
`

// BuildRebootScript renders the reboot script.
//
// force reboots whether or not the machine says one is pending; without it a
// machine with nothing to apply is left alone, so the flag is safe to pass to
// a whole selector.
func BuildRebootScript(force bool) string {
	arg := "check"
	if force {
		arg = "force"
	}
	// The script reads $1, so it is invoked with the argument inline rather
	// than relying on how the caller starts the shell.
	body := strings.NewReplacer(
		"__M_REBOOTING__", MarkerRebooting,
		"__M_NO_REBOOT__", MarkerNoReboot,
	).Replace(rebootTemplate)

	return "set -- " + arg + "\n" + body
}
