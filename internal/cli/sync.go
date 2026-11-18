package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/facts"
	"github.com/cl-tim/homelab-cli/internal/output"
)

func newSyncCmd(a *app) *cobra.Command {
	var (
		emitTags bool
		save     bool
	)

	cmd := &cobra.Command{
		Use:   "sync [selector]",
		Short: "Refresh what hlab knows about the machines it manages",
		Long: "Connects to each managed host and collects a read-only snapshot: OS,\n" +
			"kernel, uptime, CPU and memory, disks, running services, listening TCP\n" +
			"ports and any Docker or Podman containers.\n\n" +
			"With no selector it covers every managed machine. A selector narrows that\n" +
			"further; either way only managed machines are contacted, since anything\n" +
			"else has no credentials to connect with.\n\n" +
			"Everything is gathered in a single round trip per host, using only POSIX\n" +
			"shell and tools that are probed for first, so minimal systems report what\n" +
			"they can rather than failing. Nothing is installed or changed, and sudo is\n" +
			"never used — so process names against listening ports only appear when the\n" +
			"configured user can see them.\n\n" +
			"A machine that cannot be reached at its recorded address but whose name\n" +
			"now resolves elsewhere is followed there — only if it presents the host\n" +
			"key recorded for the old address, which proves it is the same machine.\n" +
			"The record and known_hosts are then updated.\n\n" +
			"Exits non-zero if any host could not be reached.",
		Example: "  hlab sync\n" +
			"  hlab sync @core\n" +
			"  hlab sync '#docker' -o json\n" +
			"  hlab sync --emit-tags",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := a.managedTargets(args)
			if err != nil {
				return err
			}

			exec, err := a.newExecutor()
			if err != nil {
				return err
			}

			if !a.format.Structured() {
				fmt.Fprintf(a.errOut, "Collecting from %d host(s)...\n", len(targets))
			}

			snapshots := facts.NewCollector(exec).Collect(cmd.Context(), targets)

			// Following a move writes the config and known_hosts, so it is
			// part of saving rather than something a dry look should do.
			if save {
				moves, retry := a.followMoves(cmd.Context(), snapshots, targets)
				a.reportMoves(moves)
				if len(retry) > 0 {
					// A fresh executor, so its host key verifier sees the
					// entries just recorded for the new addresses.
					again, err := a.newExecutor()
					if err != nil {
						return err
					}
					snapshots = replaceSnapshots(snapshots, facts.NewCollector(again).Collect(cmd.Context(), retry))
				}
			}

			if save {
				if err := a.persistFacts(snapshots); err != nil {
					fmt.Fprintf(a.errOut, "warning: %v\n", err)
				}
			}

			if emitTags {
				return a.emitFactTags(snapshots)
			}
			return a.renderFacts(snapshots)
		},
	}

	cmd.Flags().BoolVar(&emitTags, "emit-tags", false, "print suggested inventory tags derived from what each host runs")
	cmd.Flags().BoolVar(&save, "save", true, "record what was collected in the config and the facts store")

	return cmd
}

// persistFacts writes a collection to its two homes: durable attributes onto
// the host records, and volatile state into the facts store.
func (a *app) persistFacts(snapshots []facts.Facts) error {
	var (
		attrs = make([]config.HostAttributes, 0, len(snapshots))
		store = facts.Store{}
		now   = time.Now()
	)

	for _, f := range snapshots {
		if !f.OK() {
			continue
		}
		attrs = append(attrs, config.HostAttributes{
			Name:       f.Host,
			Hostname:   f.Hostname,
			Kernel:     f.Kernel,
			Arch:       f.Arch,
			CPUs:       f.CPUs,
			MemoryMB:   f.Memory.TotalKB / 1024,
			Network:    f.Network,
			OS:         f.OS.Short(),
			Containers: f.Containers,
		})
		store[f.Host] = facts.SnapshotOf(f, now)
	}

	if len(attrs) == 0 {
		return nil
	}

	var recordErr, factsErr error
	if _, err := config.UpdateHostAttributes(a.configWritePath(), attrs, now); err != nil {
		recordErr = fmt.Errorf("could not update the host records: %w", err)
	}
	if err := facts.SaveStore(config.FactsPath(), store); err != nil {
		factsErr = fmt.Errorf("could not write the facts store: %w", err)
	}
	if err := errors.Join(recordErr, factsErr); err != nil {
		return err
	}

	if !a.format.Structured() {
		fmt.Fprintf(a.errOut, "Recorded %d host(s) in %s and %s\n",
			len(attrs), a.configWritePath(), config.FactsPath())
	}
	return nil
}

// renderFacts prints the summary table plus per-host detail.
func (a *app) renderFacts(snapshots []facts.Facts) error {
	if a.format.Structured() {
		if err := output.Write(a.out, a.format, snapshots, output.Table{}); err != nil {
			return err
		}
		return factsExitCode(snapshots)
	}

	tbl := output.Table{Headers: []string{"HOST", "OS", "KERNEL", "UPTIME", "CPU", "MEM", "DISK", "SVCS", "PORTS", "CTRS"}}
	for _, f := range snapshots {
		if !f.OK() {
			tbl.AddRow(f.Host, "-", "-", "-", "-", "-", "-", "-", "-", "-")
			continue
		}

		mem := "-"
		if pct := f.Memory.UsedPercent(); pct >= 0 {
			mem = fmt.Sprintf("%d%%", pct)
		}
		disk := "-"
		if root, ok := f.RootDisk(); ok {
			disk = fmt.Sprintf("%d%%", root.UsePercent)
		}
		cpus := "-"
		if f.CPUs > 0 {
			cpus = fmt.Sprint(f.CPUs)
		}

		tbl.AddRow(f.Host, orDash(f.OS.Short()), orDash(f.Kernel), orDash(f.Uptime()),
			cpus, mem, disk, len(f.Services), len(f.Ports), len(f.Containers))
	}

	fmt.Fprintln(a.out)
	if err := output.WriteTable(a.out, tbl); err != nil {
		return err
	}

	for _, f := range snapshots {
		a.renderHostDetail(f)
	}

	failed := 0
	for _, f := range snapshots {
		if !f.OK() {
			failed++
		}
	}
	fmt.Fprintf(a.out, "\n%d/%d collected\n", len(snapshots)-failed, len(snapshots))

	return factsExitCode(snapshots)
}

// renderHostDetail prints the per-host block below the summary table.
func (a *app) renderHostDetail(f facts.Facts) {
	fmt.Fprintf(a.out, "\n=== %s ===\n", f.Host)

	if !f.OK() {
		fmt.Fprintf(a.out, "  error: %s\n", f.Error)
		return
	}
	if f.Partial {
		fmt.Fprintln(a.out, "  note: output was truncated; the snapshot may be incomplete")
	}
	// Explain a thin result rather than leaving it looking like a quiet machine.
	if f.Privilege == "none" {
		fmt.Fprintln(a.out, "  note: no sudo available, so process names and containers may be missing")
	}

	// A record whose name came from reverse DNS can be wrong, and wrong in a
	// way that looks deliberate. Say so rather than leaving it to be noticed.
	if f.Hostname != "" && !sameMachineName(f.Host, f.Hostname) {
		fmt.Fprintf(a.out, "  note: this machine calls itself %q, but is recorded as %q\n",
			f.Hostname, f.Host)
		fmt.Fprintf(a.out, "        rename it with: hlab hosts rename %s %s\n",
			f.Host, sanitiseInventoryName(shortHostname(f.Hostname)))
	}

	a.renderNetwork(f)

	if len(f.Ports) > 0 {
		fmt.Fprintln(a.out, "  listening:")
		for _, p := range f.Ports {
			line := fmt.Sprintf("    %-6d %-16s", p.Port, p.Scope())
			if p.Process != "" {
				line += " " + p.Process
			}
			fmt.Fprintln(a.out, strings.TrimRight(line, " "))
		}
	} else {
		fmt.Fprintln(a.out, "  listening: none visible")
	}

	if len(f.Containers) > 0 {
		fmt.Fprintln(a.out, "  containers:")
		for _, c := range f.Containers {
			fmt.Fprintf(a.out, "    %-24s %-32s %s\n", c.Name, c.Image, c.Status)
		}
	}

	if len(f.Services) > 0 {
		fmt.Fprintln(a.out, "  services:")
		for _, s := range f.Services {
			fmt.Fprintf(a.out, "    %-32s %s\n", s.ShortName(), s.Description)
		}
	}

	if len(f.Disks) > 0 {
		fmt.Fprintln(a.out, "  disks:")
		for _, d := range f.Disks {
			fmt.Fprintf(a.out, "    %-20s %-8s %3d%% used  %s free\n",
				d.MountPoint, d.Type, d.UsePercent, humanKB(d.AvailKB))
		}
	}
}

// renderNetwork prints how a machine is attached to the network: what each
// interface is, whether it is actually carrying a link, and how it got its
// address.
func (a *app) renderNetwork(f facts.Facts) {
	net := f.Network
	if net == nil || len(net.Interfaces) == 0 {
		return
	}

	header := "  network:"
	if net.Manager != "" {
		header += "  (managed by " + net.Manager + ")"
	}
	fmt.Fprintln(a.out, header)

	for _, iface := range net.Interfaces {
		link := "no link"
		switch {
		case iface.Working():
			link = "up"
			if iface.SpeedMbps > 0 {
				link = fmt.Sprintf("up %s", humanSpeed(iface.SpeedMbps))
			}
		case iface.Up:
			link = "up, no carrier"
		default:
			link = "down"
		}

		addressing := "no address"
		if len(iface.Addresses) > 0 {
			addressing = "static"
			if iface.DHCP {
				addressing = "dhcp"
			}
		}

		fmt.Fprintf(a.out, "    %-10s %-9s %-15s %-6s %s\n",
			iface.Name, orDash(iface.Type), link, addressing,
			strings.Join(iface.Addresses, ", "))
	}

	if gw := net.Gateway; gw != "" {
		fmt.Fprintf(a.out, "    gateway    %s\n", gw)
	}

	// The case this whole feature exists to find.
	if net.WiFiRedundant() {
		var wifi, eth []string
		for _, i := range net.ActiveWiFi() {
			wifi = append(wifi, i.Name)
		}
		for _, i := range net.WorkingEthernet() {
			eth = append(eth, i.Name)
		}
		fmt.Fprintf(a.out, "    note: wifi (%s) is active while ethernet (%s) is carrying a link\n",
			strings.Join(wifi, ", "), strings.Join(eth, ", "))
	}
}

// humanSpeed renders a link speed in the units people quote.
func humanSpeed(mbps int) string {
	if mbps >= 1000 && mbps%1000 == 0 {
		return fmt.Sprintf("%dG", mbps/1000)
	}
	return fmt.Sprintf("%dM", mbps)
}

// emitFactTags prints suggested tags per host, for pasting into the inventory.
func (a *app) emitFactTags(snapshots []facts.Facts) error {
	type suggestion struct {
		Host string   `json:"host" yaml:"host"`
		Tags []string `json:"tags" yaml:"tags"`
	}

	var out []suggestion
	for _, f := range snapshots {
		if !f.OK() {
			continue
		}
		if tags := f.SuggestedTags(); len(tags) > 0 {
			out = append(out, suggestion{Host: f.Host, Tags: tags})
		}
	}

	if a.format.Structured() {
		if err := output.Write(a.out, a.format, out, output.Table{}); err != nil {
			return err
		}
		return factsExitCode(snapshots)
	}

	if len(out) == 0 {
		fmt.Fprintln(a.errOut, "No tags could be derived.")
		return factsExitCode(snapshots)
	}

	fmt.Fprintln(a.out, "# Suggested tags, derived from what each host is running.")
	fmt.Fprintln(a.out, "# Merge these into the matching host entries in your config.")
	for _, s := range out {
		fmt.Fprintf(a.out, "# %s\n    tags: [%s]\n", s.Host, strings.Join(s.Tags, ", "))
	}

	return factsExitCode(snapshots)
}

// factsExitCode returns a non-zero exit when any host failed to report.
func factsExitCode(snapshots []facts.Facts) error {
	for _, f := range snapshots {
		if !f.OK() {
			return &ExitError{Code: 1}
		}
	}
	return nil
}

// humanKB renders a kibibyte count as a compact size string.
func humanKB(kb int64) string {
	const unit = 1024
	switch {
	case kb <= 0:
		return "-"
	case kb < unit:
		return fmt.Sprintf("%dK", kb)
	case kb < unit*unit:
		return fmt.Sprintf("%.1fM", float64(kb)/unit)
	case kb < unit*unit*unit:
		return fmt.Sprintf("%.1fG", float64(kb)/(unit*unit))
	default:
		return fmt.Sprintf("%.1fT", float64(kb)/(unit*unit*unit))
	}
}
