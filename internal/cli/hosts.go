package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/inventory"
	"github.com/cl-tim/homelab-cli/internal/output"
)

// hostView is the serialisation shape for host listings.
type hostView struct {
	Name         string    `json:"name" yaml:"name"`
	Address      string    `json:"address" yaml:"address"`
	Port         int       `json:"port" yaml:"port"`
	Managed      bool      `json:"managed" yaml:"managed"`
	User         string    `json:"user,omitempty" yaml:"user,omitempty"`
	IdentityFile string    `json:"identityFile,omitempty" yaml:"identityFile,omitempty"`
	Tags         []string  `json:"tags,omitempty" yaml:"tags,omitempty"`
	Description  string    `json:"description,omitempty" yaml:"description,omitempty"`
	Hostname     string    `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	OS           string    `json:"os,omitempty" yaml:"os,omitempty"`
	Banner       string    `json:"banner,omitempty" yaml:"banner,omitempty"`
	FirstSeen    time.Time `json:"firstSeen,omitempty" yaml:"firstSeen,omitempty"`
	LastSeen     time.Time `json:"lastSeen,omitempty" yaml:"lastSeen,omitempty"`
}

func newHostView(h inventory.Host) hostView {
	return hostView{
		Name:         h.Name,
		Address:      h.Address,
		Port:         h.Port,
		Managed:      h.Managed,
		User:         h.User,
		IdentityFile: h.IdentityFile,
		Tags:         h.Tags,
		Description:  h.Description,
		Hostname:     h.Hostname,
		OS:           h.OS,
		Banner:       h.Banner,
		FirstSeen:    h.FirstSeen,
		LastSeen:     h.LastSeen,
	}
}

func newHostsCmd(a *app) *cobra.Command {
	// `hlab hosts` is the listing itself, not a bare group that makes you type
	// `list` to see anything. The subcommand stays for explicitness and for
	// anything scripted against it.
	cmd := newHostsListCmd(a)
	cmd.Use = "hosts [selector]"
	cmd.Aliases = []string{"host"}

	cmd.AddCommand(newHostsListCmd(a), newHostsGroupsCmd(a),
		newHostsRenameCmd(a), newHostsPruneCmd(a), newHostsHistoryCmd(a))
	return cmd
}

func newHostsListCmd(a *app) *cobra.Command {
	var (
		managedOnly   bool
		unmanagedOnly bool
	)

	cmd := &cobra.Command{
		Use:   "list [selector]",
		Short: "List every known machine, managed or not",
		Long: "Lists machines hlab knows about: those it manages, and those a scan has\n" +
			"seen but which have not been enrolled.\n\n" +
			"This command does not connect to anything — use `hlab ping` for that.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var hosts []inventory.Host

			if len(args) == 0 {
				hosts = a.cfg.Inventory.Hosts()
			} else {
				// Listing shows unmanaged machines too, so it resolves the
				// selector against everything rather than only what is reachable.
				var err error
				hosts, err = a.cfg.Inventory.SelectAll(args[0])
				if err != nil {
					return err
				}
			}

			hosts = filterByManagement(hosts, managedOnly, unmanagedOnly)

			if len(hosts) == 0 {
				fmt.Fprintln(a.errOut, "Nothing to show.")
				fmt.Fprintln(a.errOut, "Find what is on your network with: hlab discover")
				fmt.Fprintln(a.errOut, "Then bring machines under management with: hlab discover --enroll")
				return &ExitError{Code: 1}
			}

			views := make([]hostView, 0, len(hosts))
			tbl := output.Table{Headers: []string{"NAME", "ADDRESS", "MANAGED", "USER", "OS", "TAGS", "LAST SEEN"}}
			for _, h := range hosts {
				views = append(views, newHostView(h))

				addr := h.Address
				if h.Port != 22 {
					addr = fmt.Sprintf("%s:%d", h.Address, h.Port)
				}
				managed := "no"
				if h.Managed {
					managed = "yes"
				}
				tbl.AddRow(h.Name, addr, managed, orDash(h.User), orDash(h.OS),
					orDash(strings.Join(h.Tags, ",")), orDash(relativeTime(h.LastSeen)))
			}

			return output.Write(a.out, a.format, views, tbl)
		},
	}

	cmd.Flags().BoolVar(&managedOnly, "managed", false, "show only machines under management")
	cmd.Flags().BoolVar(&unmanagedOnly, "unmanaged", false, "show only machines that have been seen but not enrolled")

	return cmd
}

// filterByManagement narrows a listing to one side of the managed divide.
func filterByManagement(hosts []inventory.Host, managedOnly, unmanagedOnly bool) []inventory.Host {
	if managedOnly == unmanagedOnly {
		return hosts // both or neither: no filtering
	}

	out := make([]inventory.Host, 0, len(hosts))
	for _, h := range hosts {
		if h.Managed == managedOnly {
			out = append(out, h)
		}
	}
	return out
}

// relativeTime renders how long ago something was seen, for table display.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	}
}

func newHostsGroupsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "groups",
		Short: "List host groups and their members",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			groups := a.cfg.Inventory.Groups()

			if len(groups) == 0 {
				fmt.Fprintln(a.errOut, "No groups defined. Add a `groups:` section to your config.")
				return &ExitError{Code: 1}
			}

			tbl := output.Table{Headers: []string{"GROUP", "HOSTS", "MEMBERS"}}
			for _, name := range a.cfg.Inventory.GroupNames() {
				members := groups[name]
				tbl.AddRow(name, len(members), strings.Join(members, ","))
			}

			return output.Write(a.out, a.format, groups, tbl)
		},
	}
}
