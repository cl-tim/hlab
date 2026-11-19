package cli

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/discover"
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
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
				type inventoryFragment struct {
					Printers []inventory.Printer `json:"printers" yaml:"printers"`
				}
				fragment := inventoryFragment{}
				used := map[string]int{}
				for _, result := range found {
					name := discoveredName(result, used)
					fragment.Printers = append(fragment.Printers, inventory.Printer{
						Name: name, Address: result.Address, Firmware: inventory.FirmwareAuto,
						MoonrakerPort: port, Tags: []string{"discovered"},
					})
				}
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
	scan.Flags().BoolVar(&emit, "emit", false, "emit a YAML inventory fragment")
	command.AddCommand(scan)
	return command
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
