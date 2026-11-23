package cli

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/cl-tim/hlab/internal/dnssync"
	printerconfig "github.com/cl-tim/hlab/internal/printerlab/config"
	"github.com/cl-tim/hlab/internal/tlsmgr"
	"github.com/spf13/cobra"
)

func newTLSDNSCmd(a *app) *cobra.Command {
	var zone string
	var apply bool
	cmd := &cobra.Command{Use: "dns <dns-host>", Short: "Publish TLS names at their actual device addresses", Long: "Publish the CA and TLS service names to Technitium, preserving unrelated records.\nOnly declared TLS names are changed; SSH host records cannot be redirected to another device.\nThe selected DNS host must hold a writable primary zone. Replicated secondary zones\nreceive the update through their normal zone transfer.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.tlsConfig()
		if err != nil {
			return err
		}
		if err = dnssync.ValidateZone(zone); err != nil {
			return err
		}
		h, err := a.oneManagedHost(args)
		if err != nil {
			return err
		}
		want, err := a.tlsDNSRecords(cmd.Context(), c, zone)
		if err != nil {
			return err
		}
		d, err := a.newDialer()
		if err != nil {
			return err
		}
		script, err := dnssync.BuildReadScript(zone)
		if err != nil {
			return err
		}
		res, err := runPrivilegedOn(cmd.Context(), d, targetFor(h), script)
		if err != nil {
			return err
		}
		if !res.OK() {
			return fmt.Errorf("read DNS: %s", res.Stderr)
		}
		have, err := dnssync.ParseRecords(res.Stdout)
		if err != nil {
			return err
		}
		plan := dnssync.Diff(zone, want, have)
		if apply && plan.Actionable() {
			if a.insecureHostKey {
				return fmt.Errorf("TLS DNS changes require verified SSH host keys")
			}
			script, err = dnssync.BuildSyncScript(zone, plan.Changes, nil)
			if err != nil {
				return err
			}
			res, err = runPrivilegedOn(cmd.Context(), d, targetFor(h), script)
			if err != nil {
				return err
			}
			if !res.OK() || !strings.Contains(res.Stdout, dnssync.MarkerDone) {
				return fmt.Errorf("publish TLS DNS: %s", res.Stderr)
			}
		}
		return a.reportDNSPlan(plan, false)
	}}
	cmd.Flags().StringVar(&zone, "zone", "lan", "primary DNS zone to update")
	cmd.Flags().BoolVar(&apply, "apply", false, "publish changes; otherwise read and preview")
	return cmd
}

func (a *app) tlsDNSRecords(ctx context.Context, c tlsmgr.Config, zone string) ([]dnssync.Record, error) {
	hosts, err := a.tlsTargets(nil, false)
	if err != nil {
		return nil, err
	}
	var all = hosts
	needsPrinters := false
	for _, s := range c.Services {
		if strings.HasPrefix(s.Target, "printer:") {
			needsPrinters = true
		}
	}
	if needsPrinters {
		ps, e := a.tlsTargets(nil, true)
		if e != nil {
			return nil, e
		}
		all = append(all, ps...)
	}
	byRef := map[string]string{}
	for _, t := range all {
		byRef[t.Ref] = t.Target.Address
	}
	want := map[string]string{}
	resolved := map[string]net.IP{}
	resolve := func(address string) (net.IP, error) {
		if ip, ok := resolved[address]; ok {
			return ip, nil
		}
		ip := net.ParseIP(address)
		if ip == nil {
			ips, e := net.DefaultResolver.LookupIPAddr(ctx, address)
			if e != nil {
				return nil, e
			}
			for _, i := range ips {
				if i.IP.To4() != nil {
					if ip != nil && !ip.Equal(i.IP) {
						return nil, fmt.Errorf("%s resolves to several IPv4 addresses; set a stable inventory address", address)
					}
					ip = i.IP
				}
			}
		}
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("TLS DNS requires an IPv4 device address: %s", address)
		}
		resolved[address] = ip
		return ip, nil
	}
	add := func(name, ref string) error {
		if !strings.HasSuffix(name, "."+zone) {
			return fmt.Errorf("TLS name %q lies outside zone %s", name, zone)
		}
		address, ok := byRef[ref]
		if !ok {
			return fmt.Errorf("unknown TLS target %s", ref)
		}
		ip, e := resolve(address)
		if e != nil {
			return fmt.Errorf("resolve TLS device %s: %w", ref, e)
		}
		want[name] = ip.String()
		return nil
	}
	if err = add(c.AuthorityDNS, "host:"+c.Authority); err != nil {
		return nil, err
	}
	for _, s := range c.Services {
		for _, name := range s.Names {
			if err = add(name, s.Target); err != nil {
				return nil, err
			}
		}
	}
	// A service name matching an enrolled host must still reach that host. A
	// separate service alias such as hass.lan can move to its actual device.
	protect := func(name, address string) error {
		if v, ok := want[name]; ok {
			ip, e := resolve(address)
			if e != nil {
				return e
			}
			if !ip.Equal(net.ParseIP(v)) {
				return fmt.Errorf("refusing to redirect enrolled hostname %s away from %s", name, address)
			}
		}
		return nil
	}
	for _, h := range a.cfg.Inventory.Managed() {
		if err = protect(h.Name+"."+zone, h.Address); err != nil {
			return nil, err
		}
	}
	printers, err := printerconfig.Load(a.cfg.Path)
	if err != nil {
		return nil, err
	}
	for _, p := range printers.Inventory.Printers() {
		if err = protect(p.Name+"."+zone, p.Address); err != nil {
			return nil, err
		}
		if strings.HasSuffix(p.Address, "."+zone) {
			if err = protect(p.Address, p.Address); err != nil {
				return nil, err
			}
		}
	}
	var out []dnssync.Record
	for n, v := range want {
		out = append(out, dnssync.Record{Name: n, Address: v})
	}
	return out, nil
}
