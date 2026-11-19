package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cl-tim/hlab/internal/inventory"
	"github.com/cl-tim/hlab/internal/sshx"
	"github.com/cl-tim/hlab/internal/updates"
)

// resolverDependents reports managed machines whose only recorded resolvers
// are addresses belonging to the given host.
//
// Rebooting the machine those depend on takes their name resolution with it
// for the duration. hlab itself is unaffected — it connects by address — which
// is exactly why this needs checking deliberately rather than being noticed
// when something else breaks.
//
// A machine resolving through a local stub (127.0.0.53) hides its real
// upstreams behind systemd-resolved, so it cannot be judged from here and is
// not counted. The check finds the case it can prove, not every case.
func resolverDependents(all []inventory.Host, target inventory.Host) []string {
	mine := addressesOf(target)
	if len(mine) == 0 {
		return nil
	}

	var stranded []string
	for _, h := range all {
		if h.Name == target.Name || !h.Managed || h.Network == nil {
			continue
		}
		servers := h.Network.DNS
		if len(servers) == 0 {
			continue
		}

		other := false
		for _, s := range servers {
			if isLoopback(s) {
				// A stub resolver says nothing about where queries really go.
				other = true
				break
			}
			if !mine[s] {
				other = true
				break
			}
		}
		if !other {
			stranded = append(stranded, h.Name)
		}
	}
	return stranded
}

// addressesOf collects every address a host is known to hold.
func addressesOf(h inventory.Host) map[string]bool {
	out := map[string]bool{}
	if h.Address != "" {
		out[h.Address] = true
	}
	if h.Network == nil {
		return out
	}
	for _, iface := range h.Network.Interfaces {
		for _, addr := range iface.Addresses {
			ip, _, _ := strings.Cut(addr, "/")
			if ip != "" {
				out[ip] = true
			}
		}
	}
	return out
}

func isLoopback(addr string) bool {
	return strings.HasPrefix(addr, "127.") || addr == "::1"
}

// rebootHost restarts a machine and waits for it to come back, proving the
// reboot happened rather than assuming it.
func (a *app) rebootHost(
	ctx context.Context, dialer *sshx.Dialer, h inventory.Host, all []inventory.Host, force bool,
) error {
	if stranded := resolverDependents(all, h); len(stranded) > 0 {
		return fmt.Errorf(
			"refusing to reboot: %s is the only recorded resolver for %s.\n"+
				"Give %s a second resolver first, or reboot it yourself when that is acceptable",
			h.Name, strings.Join(stranded, ", "), strings.Join(stranded, ", "))
	}

	target := targetFor(h)

	// Read the boot id first. Without it, "the machine answered again" is
	// indistinguishable from "the machine never went down".
	before, err := a.readBootID(ctx, dialer, target)
	if err != nil {
		return fmt.Errorf("could not read the boot id before rebooting: %w", err)
	}

	res, err := runPrivilegedOn(ctx, dialer, target, updates.BuildRebootScript(force))
	switch {
	case err != nil && !sshx.ConnectionLost(err):
		return err
	case err != nil:
		// The reboot is detached so this should not happen, but a machine that
		// went early has still done what was asked.
		fmt.Fprintln(a.errOut, "  the connection dropped as the reboot was issued")
	default:
		if strings.Contains(res.Stdout, updates.MarkerNoReboot) {
			fmt.Fprintf(a.errOut, "  %s does not need a reboot\n", h.Name)
			return nil
		}
		if !strings.Contains(res.Stdout, updates.MarkerRebooting) {
			msg := firstNonEmptyLine(res.Stderr)
			if msg == "" {
				msg = fmt.Sprintf("the reboot did not report success (exit %d)", res.ExitCode)
			}
			return fmt.Errorf("%s", strings.TrimPrefix(msg, "hlab:error "))
		}
	}

	fmt.Fprintf(a.errOut, "  rebooting; waiting up to %s for it to come back...\n",
		updates.RebootTimeout.Round(time.Minute))
	return a.waitForReboot(ctx, dialer, h, target, before)
}

// waitForReboot polls until the machine answers with a different boot id.
func (a *app) waitForReboot(
	ctx context.Context, dialer *sshx.Dialer, h inventory.Host, target sshx.Target, before string,
) error {
	deadline := time.Now().Add(updates.RebootTimeout)

	for {
		select {
		case <-time.After(10 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}

		if time.Now().After(deadline) {
			return fmt.Errorf(
				"%s did not come back within %s. It may still be booting; check it before acting",
				h.Name, updates.RebootTimeout)
		}

		after, err := a.readBootID(ctx, dialer, target)
		if err != nil || after == "" {
			continue // still down, or not answering yet
		}
		if after == before {
			// Answering, but on the same boot: the reboot has not happened yet.
			continue
		}

		fmt.Fprintf(a.errOut, "  %s is back\n", h.Name)
		return nil
	}
}

// readBootID fetches the identifier the kernel generates once per boot.
func (a *app) readBootID(ctx context.Context, dialer *sshx.Dialer, t sshx.Target) (string, error) {
	client, err := dialer.Dial(ctx, t)
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()

	res, err := sshx.Exec(ctx, client, updates.BootIDScript, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}
