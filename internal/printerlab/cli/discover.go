package cli

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/discover"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

func newDiscoverCmd(a *app) *cobra.Command {
	command := &cobra.Command{Use: "discover", Short: "Find Moonraker printers on the network"}
	var port, concurrency int
	var timeout time.Duration
	var emit bool
	scan := &cobra.Command{
		Use:   "scan [CIDR...]",
		Short: "Scan local or specified IPv4 subnets for Moonraker",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cidrs := args
			if len(cidrs) == 0 {
				var err error
				cidrs, err = discover.LocalSubnets()
				if err != nil {
					return err
				}
				if !a.format.Structured() {
					_, _ = fmt.Fprintf(a.errOut, "Scanning local subnet(s): %s\n", strings.Join(cidrs, ", "))
				}
			}
			found, err := discover.Scan(cmd.Context(), cidrs, port, timeout, concurrency)
			if err != nil {
				return err
			}
			if emit {
				fragment := emitFragment(found, port)
				format := a.format
				if format == output.FormatTable {
					format = output.FormatYAML
				}
				return output.Write(a.out, format, fragment, output.Table{})
			}
			table := output.Table{Headers: []string{"ADDRESS", "HOSTNAME", "KLIPPY", "MOONRAKER"}}
			for _, result := range found {
				table.AddRow(result.Address, result.Hostname, result.KlippyState, result.MoonrakerVersion)
			}
			if err := output.Write(a.out, a.format, found, table); err != nil {
				return err
			}
			if len(found) == 0 && !a.format.Structured() {
				_, _ = fmt.Fprintln(a.errOut, "No Moonraker printers found.")
			}
			return nil
		},
	}
	scan.Flags().IntVar(&port, "port", 7125, "Moonraker TCP port")
	scan.Flags().IntVar(&concurrency, "probe-concurrency", 64, "concurrent probes")
	scan.Flags().DurationVar(&timeout, "timeout", 750*time.Millisecond, "per-address timeout")
	scan.Flags().BoolVar(&emit, "emit", false, "emit a `printerlab:` fragment for the hlab config")
	command.AddCommand(scan)
	return command
}

// inventoryFragment is what `discover scan --emit` prints: the printers
// found, under the section of the hlab config they belong in, so the output
// merges into config.yaml as it stands. The tag must stay config.SectionKey;
// TestEmittedFragmentLoadsAsAnInventory holds it there.
type inventoryFragment struct {
	Printerlab struct {
		Printers []inventory.Printer `json:"printers" yaml:"printers"`
	} `json:"printerlab" yaml:"printerlab"`
}

func emitFragment(found []discover.Result, port int) inventoryFragment {
	fragment := inventoryFragment{}
	fragment.Printerlab.Printers = make([]inventory.Printer, 0, len(found))
	used := map[string]int{}
	for _, result := range found {
		fragment.Printerlab.Printers = append(fragment.Printerlab.Printers, inventory.Printer{
			Name: discoveredName(result, used), Address: result.Address, Firmware: inventory.FirmwareAuto,
			MoonrakerPort: port, Tags: []string{"discovered"},
		})
	}
	return fragment
}

var invalidName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func discoveredName(result discover.Result, used map[string]int) string {
	name := strings.TrimSpace(result.Hostname)
	if name == "" {
		name = "printer-" + strings.ReplaceAll(result.Address, ".", "-")
	}
	name = strings.Trim(invalidName.ReplaceAllString(name, "-"), "-")
	if name == "" {
		name = "printer"
	}
	used[name]++
	if used[name] > 1 {
		name = fmt.Sprintf("%s-%d", name, used[name])
	}
	return name
}
