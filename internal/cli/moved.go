package cli

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/facts"
	"github.com/cl-tim/hlab/internal/inventory"
	"github.com/cl-tim/hlab/internal/sshx"
)

// movedHost is a machine found answering somewhere other than its record.
type movedHost struct {
	Name, From, To string
	// Absorbed is the unmanaged record a scan had made for the new address.
	Absorbed string
}

// followMoves looks for failed machines that have moved, and repoints their
// records at where they now are.
//
// hlab connects by recorded address, so a machine whose DHCP lease changed
// fails every command — including sync, the one that would otherwise learn
// about it. Its name, though, usually still resolves: the DHCP server
// registers it. A new address found that way is followed only when the machine
// there proves it holds the host key recorded for the old one
// (sshx.DialMoved); a name pointing somewhere else proves nothing on its own.
//
// It returns the moves made, and targets for re-collecting those machines.
func (a *app) followMoves(ctx context.Context, snapshots []facts.Facts, targets []sshx.Target) ([]movedHost, []sshx.Target) {
	byName := make(map[string]sshx.Target, len(targets))
	for _, t := range targets {
		byName[t.Name] = t
	}
	records := make(map[string]inventory.Host)
	for _, h := range a.cfg.Inventory.Hosts() {
		records[h.Name] = h
	}

	var (
		moves []movedHost
		retry []sshx.Target
		// Built lazily: most syncs have no failures to look into.
		dialer *sshx.Dialer
	)
	for _, f := range snapshots {
		if f.Error == "" {
			continue
		}
		t, ok := byName[f.Host]
		if !ok {
			continue
		}
		candidates := resolvedElsewhere(ctx, records[f.Host], t.Address)
		if len(candidates) == 0 {
			continue
		}
		if dialer == nil {
			var err error
			if dialer, err = a.newDialer(); err != nil {
				return moves, retry
			}
		}

		for _, addr := range candidates {
			client, err := dialer.DialMoved(ctx, t, addr)
			if err != nil {
				fmt.Fprintf(a.errOut, "%s: %s resolves to %s, but it was not followed: %s\n",
					t.Name, records[f.Host].Hostname, addr, firstNonEmptyLine(err.Error()))
				continue
			}
			_ = client.Close()

			absorbed, err := config.MoveHost(a.configWritePath(), t.Name, addr)
			if err != nil {
				fmt.Fprintf(a.errOut, "%s: found at %s but the record could not be updated: %v\n", t.Name, addr, err)
				break
			}
			if absorbed != "" {
				_ = facts.RemoveFromStore(config.FactsPath(), absorbed)
			}
			moves = append(moves, movedHost{Name: t.Name, From: t.Address, To: addr, Absorbed: absorbed})
			t.Address = addr
			retry = append(retry, t)
			break
		}
	}
	return moves, retry
}

// resolvedElsewhere returns the addresses a machine's names now resolve to,
// other than the one it is recorded at.
func resolvedElsewhere(ctx context.Context, h inventory.Host, current string) []string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	seen := map[string]bool{current: true}
	var out []string
	for _, name := range []string{h.Hostname, h.Name} {
		if name == "" {
			continue
		}
		addrs, err := net.DefaultResolver.LookupHost(ctx, name)
		if err != nil {
			continue
		}
		for _, s := range addrs {
			ip, err := netip.ParseAddr(s)
			if err != nil || ip.IsLoopback() || ip.IsUnspecified() || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// reportMoves says what followMoves changed.
func (a *app) reportMoves(moves []movedHost) {
	for _, m := range moves {
		fmt.Fprintf(a.errOut, "%s moved %s -> %s: it presented the host key recorded for %s, so the record now points there.\n",
			m.Name, m.From, m.To, m.From)
		if m.Absorbed != "" {
			fmt.Fprintf(a.errOut, "  %s was a scan's record of the same machine and has been removed.\n", m.Absorbed)
		}
	}
	if len(moves) > 0 {
		fmt.Fprintln(a.errOut, "A machine on a DHCP address will move again; a reservation or `hlab net static` stops that.")
	}
}

// replaceSnapshots swaps in re-collected snapshots by host name.
func replaceSnapshots(all, fresh []facts.Facts) []facts.Facts {
	byHost := make(map[string]facts.Facts, len(fresh))
	for _, f := range fresh {
		byHost[f.Host] = f
	}
	for i, f := range all {
		if r, ok := byHost[f.Host]; ok {
			all[i] = r
		}
	}
	return all
}
