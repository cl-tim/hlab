package cli

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/inventory"
	"github.com/cl-tim/homelab-cli/internal/netcfg"
	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/sshx"
)

func newNetCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "net",
		Short: "Change how managed machines are attached to the network",
	}
	cmd.AddCommand(newNetStaticCmd(a), newNetWiFiOffCmd(a))
	return cmd
}

func newNetStaticCmd(a *app) *cobra.Command {
	var (
		apply  bool
		to     string
		pool   string
		dns    []string
		search []string
	)

	cmd := &cobra.Command{
		Use:   "static [selector]",
		Short: "Pin machines to the address they already hold",
		Long: "Converts the interface hlab reaches a machine over from a DHCP lease to a\n" +
			"static address.\n\n" +
			"The address written is the one the machine already has. Moving to a\n" +
			"different address at the same time would mean the verification connection\n" +
			"has to find the machine somewhere new, which turns a reversible change into\n" +
			"a gamble — do that separately if you want it.\n\n" +
			"Each change is applied behind a dead-man timer: the machine puts its old\n" +
			"configuration back after " + fmt.Sprint(netcfg.RollbackSeconds) + "s unless hlab reconnects and confirms. A\n" +
			"change that cuts the connection therefore undoes itself without anyone\n" +
			"having to reach the machine.\n\n" +
			"--dns replaces the machine's resolvers. That is the same write to the same\n" +
			"file, so it is part of this command rather than a separate one, and it is\n" +
			"the one change here that cannot strand a machine: hlab connects by address,\n" +
			"not by name. Machines already holding a static address are updated too.\n\n" +
			"Nothing is changed unless --apply is given.",
		Example: "  hlab net static\n  hlab net static apps-1 --apply",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := a.managedHosts(args)
			if err != nil {
				return err
			}

			plans, err := a.planStatic(hosts, to, pool, dns, search)
			if err != nil {
				return err
			}
			return a.runNetPlans(cmd.Context(), plans, apply, netcfg.BuildStaticScript)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&apply, "apply", false, "actually make the change; without it nothing is touched")
	f.StringVar(&to, "to", "", "move the machine to this address instead of keeping its lease")
	f.StringVar(&pool, "pool", "", "the DHCP range to stay out of, e.g. 192.0.2.50-192.0.2.250")
	f.StringSliceVar(&dns, "dns", nil,
		"resolvers to write, in order; also updates machines that are already static")
	f.StringSliceVar(&search, "dns-search", nil,
		"domain search list, so short names keep resolving (default: what the machine already has)")
	return cmd
}

func newNetWiFiOffCmd(a *app) *cobra.Command {
	var apply bool

	cmd := &cobra.Command{
		Use:   "wifi-off [selector]",
		Short: "Disable wireless on machines that have working ethernet",
		Long: "Blocks wireless with rfkill on machines that are reached over ethernet.\n\n" +
			"A machine is only touched when it has a working ethernet link *and* hlab\n" +
			"already reaches it over that link. Disabling the interface the connection\n" +
			"is running through would end the session and leave no way back in, so that\n" +
			"case is refused rather than attempted.\n\n" +
			"rfkill is used rather than editing the network configuration: it is the same\n" +
			"on every distribution, survives a reboot, and is undone with a single\n" +
			"unblock. As with a static address, the change reverts itself if hlab cannot\n" +
			"reconnect and confirm.\n\n" +
			"Nothing is changed unless --apply is given.",
		Example: "  hlab net wifi-off\n  hlab net wifi-off apps-1 --apply",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := a.managedHosts(args)
			if err != nil {
				return err
			}

			plans := make([]netcfg.Plan, 0, len(hosts))
			for _, h := range hosts {
				plans = append(plans, netcfg.PlanDisableWiFi(h))
			}
			return a.runNetPlans(cmd.Context(), plans, apply, netcfg.BuildWiFiScript)
		},
	}

	cmd.Flags().BoolVar(&apply, "apply", false, "actually make the change; without it nothing is touched")
	return cmd
}

// runNetPlans reports what would happen and, with apply, carries it out one
// machine at a time.
//
// Changes are deliberately serial. Applying to a fleet in parallel means a
// mistake in the plan reaches every machine before the first failure is
// visible; one at a time, the first failure stops the rest.
func (a *app) runNetPlans(
	ctx context.Context,
	plans []netcfg.Plan,
	apply bool,
	build func(netcfg.Plan) (string, error),
) error {
	tbl := output.Table{Headers: []string{"HOST", "ACTION", "DETAIL"}}
	var actionable []netcfg.Plan

	for _, p := range plans {
		if p.Skipped {
			tbl.AddRow(p.Host.Name, "skip", p.Reason)
			continue
		}
		tbl.AddRow(p.Host.Name, string(p.Action), p.Reason)
		actionable = append(actionable, p)
	}

	if err := output.WriteTable(a.out, tbl); err != nil {
		return err
	}

	if len(actionable) == 0 {
		fmt.Fprintln(a.out, "\nNothing to do.")
		return nil
	}
	if !apply {
		fmt.Fprintf(a.out, "\n%d machine(s) would be changed. Re-run with --apply.\n", len(actionable))
		return nil
	}

	dialer, err := a.newDialer()
	if err != nil {
		return err
	}

	var changed, failed int
	for _, p := range actionable {
		fmt.Fprintf(a.errOut, "\n%s: %s\n", p.Host.Name, p.Reason)

		if err := a.applyNetPlan(ctx, dialer, p, build); err != nil {
			failed++
			fmt.Fprintf(a.errOut, "  failed: %v\n", err)
			fmt.Fprintf(a.errOut,
				"  the machine will restore its previous configuration within %ds\n", netcfg.RollbackSeconds)
			fmt.Fprintln(a.errOut, "  stopping here rather than repeating this on the rest")
			break
		}
		changed++
		fmt.Fprintln(a.errOut, "  confirmed")
	}

	fmt.Fprintf(a.out, "\n%d changed, %d failed\n", changed, failed)
	if failed > 0 {
		return &ExitError{Code: 1}
	}
	return nil
}

// applyNetPlan runs the change, reconnects to prove the machine is still
// reachable, and only then cancels the revert.
func (a *app) applyNetPlan(
	ctx context.Context,
	dialer *sshx.Dialer,
	p netcfg.Plan,
	build func(netcfg.Plan) (string, error),
) error {
	script, err := build(p)
	if err != nil {
		return err
	}

	target := targetFor(p.Host)

	res, err := runPrivilegedOn(ctx, dialer, target, script)
	switch {
	case err != nil && !sshx.ConnectionLost(err):
		return err

	case err != nil:
		// Reapplying the interface hlab is connected over ends the session
		// before the script can say anything, so this decides nothing either
		// way — the verification below settles it by looking at the machine.
		// The revert is already armed, so a change that genuinely broke the
		// machine is still undone whichever conclusion is reached here.
		fmt.Fprintln(a.errOut,
			"  the connection dropped while applying, which this change is expected to do")

	default:
		if !strings.Contains(res.Stdout, netcfg.MarkerApplied) &&
			!strings.Contains(res.Stdout, netcfg.MarkerNoop) {
			msg := firstNonEmptyLine(res.Stderr)
			if msg == "" {
				msg = fmt.Sprintf("the change did not report success (exit %d)", res.ExitCode)
			}
			return fmt.Errorf("%s", strings.TrimPrefix(msg, "hlab:error "))
		}
		if strings.Contains(res.Stdout, netcfg.MarkerNoop) {
			fmt.Fprintln(a.errOut, "  already in the desired state")
			return nil
		}
	}

	// A relocation has to be verified where the machine now is, not where it
	// was. Getting this wrong would confirm a change that actually failed.
	verifyAt := target
	if p.NewAddress != "" {
		verifyAt.Address = p.NewAddress
		fmt.Fprintf(a.errOut, "  applied; looking for the machine at %s...\n", p.NewAddress)
	} else {
		fmt.Fprintln(a.errOut, "  applied; verifying the machine is still reachable...")
	}

	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	// Keep trying until shortly before the machine would revert. Giving up
	// earlier than that is worse than useless: it reports a failure while the
	// change may well have worked, and the rollback then undoes it anyway. The
	// margin is what is left to confirm in once the machine does answer.
	deadline := time.Now().Add((netcfg.RollbackSeconds - 15) * time.Second)
	if err := verifyReachable(ctx, dialer, verifyAt, deadline); err != nil {
		return fmt.Errorf("could not reconnect after the change: %w", err)
	}

	if _, err := runPrivilegedOn(ctx, dialer, verifyAt, netcfg.ConfirmScript); err != nil {
		return fmt.Errorf("reconnected but could not cancel the rollback: %w", err)
	}

	// Only once the machine has answered at the new address is the record
	// moved. Writing it earlier would leave hlab pointing at an address that
	// the rollback is about to take away.
	if p.NewAddress != "" {
		if err := config.SetHostAddress(a.configWritePath(), p.Host.Name, p.NewAddress); err != nil {
			return fmt.Errorf(
				"the machine moved to %s and answered there, but the record could not be updated: %w",
				p.NewAddress, err)
		}
		fmt.Fprintf(a.errOut, "  record updated to %s\n", p.NewAddress)
	}
	return nil
}

// verifyReachable reconnects and runs a trivial command, retrying briefly
// because an interface that has just been reconfigured takes a moment.
func verifyReachable(ctx context.Context, dialer *sshx.Dialer, t sshx.Target, deadline time.Time) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if time.Now().After(deadline) {
				return lastErr
			}
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		client, err := dialer.Dial(ctx, t)
		if err != nil {
			lastErr = err
			continue
		}
		res, err := sshx.Exec(ctx, client, "true", nil)
		_ = client.Close()
		if err == nil && res.OK() {
			return nil
		}
		lastErr = err
	}
}

// runPrivilegedOn opens a connection and runs a script as root.
func runPrivilegedOn(ctx context.Context, dialer *sshx.Dialer, t sshx.Target, script string) (sshx.ExecResult, error) {
	client, err := dialer.Dial(ctx, t)
	if err != nil {
		return sshx.ExecResult{}, err
	}
	defer func() { _ = client.Close() }()

	id, err := sshx.Exec(ctx, client, "id -u", nil)
	if err != nil {
		return sshx.ExecResult{}, err
	}

	command := "sudo -n sh -s"
	if strings.TrimSpace(id.Stdout) == "0" {
		command = "sh -s"
	}
	return sshx.Exec(ctx, client, command, strings.NewReader(script))
}

func targetFor(h inventory.Host) sshx.Target {
	return sshx.Target{
		Name:         h.Name,
		Address:      h.Address,
		Port:         h.Port,
		User:         h.User,
		IdentityFile: h.IdentityFile,
	}
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// planStatic builds the static plans, optionally relocating machines out of
// the DHCP pool.
func (a *app) planStatic(
	hosts []inventory.Host, to, poolSpec string, dns, search []string,
) ([]netcfg.Plan, error) {
	plans := make([]netcfg.Plan, 0, len(hosts))
	for _, h := range hosts {
		plans = append(plans, netcfg.PlanStaticWithDNS(h, dns, search))
	}

	if to == "" && poolSpec == "" {
		return plans, nil
	}
	if to != "" && len(hosts) != 1 {
		return nil, fmt.Errorf("--to names one address, so it takes one machine; %d matched", len(hosts))
	}

	var pool netcfg.Pool
	if poolSpec != "" {
		p, err := netcfg.ParsePool(poolSpec)
		if err != nil {
			return nil, err
		}
		pool = p
	}

	// Everything hlab knows about counts as occupied, not just managed
	// machines: an address that answered a scan will answer again.
	all := a.cfg.Inventory.Hosts()

	// One allocator per subnet, shared across the whole run. Building one per
	// machine would restart the search each time and hand every one of them
	// the same address.
	allocators := map[netip.Prefix]*netcfg.Allocator{}

	for i, p := range plans {
		if p.Skipped {
			continue
		}

		prefix, ok := netcfg.PrefixOf(interfaceNamed(p.Host, p.Interface), p.Host.Address)
		if !ok {
			plans[i] = netcfg.Skipped(p, fmt.Sprintf("could not determine the subnet of %s", p.Host.Address))
			continue
		}

		alloc, seen := allocators[prefix]
		if !seen {
			alloc = netcfg.NewAllocator(prefix, pool, all, p.Host.Network.Gateway)
			allocators[prefix] = alloc
		}

		var target netip.Addr
		if to != "" {
			addr, err := netip.ParseAddr(to)
			if err != nil {
				return nil, fmt.Errorf("invalid --to address %q: %w", to, err)
			}
			if err := alloc.Check(addr, p.Host.Name); err != nil {
				return nil, err
			}
			target = addr
		} else {
			// No explicit address: only move machines that are actually in the
			// pool. One already outside it is left where it is.
			current, err := netip.ParseAddr(p.Host.Address)
			if err != nil || !pool.Contains(current) {
				continue
			}
			addr, err := alloc.Next(p.Host.Name)
			if err != nil {
				plans[i] = netcfg.Skipped(p, err.Error())
				continue
			}
			target = addr
		}

		alloc.Reserve(target, p.Host.Name)
		relocated, err := netcfg.Relocate(p, target)
		if err != nil {
			plans[i] = netcfg.Skipped(p, err.Error())
			continue
		}
		plans[i] = relocated
	}

	return plans, nil
}

func interfaceNamed(h inventory.Host, name string) inventory.Interface {
	if h.Network != nil {
		for _, iface := range h.Network.Interfaces {
			if iface.Name == name {
				return iface
			}
		}
	}
	return inventory.Interface{}
}
