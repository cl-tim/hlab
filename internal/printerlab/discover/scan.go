// Package discover scans an IPv4 range for Moonraker endpoints.
package discover

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

const maxAddresses = 4096

type Result struct {
	Address          string `json:"address" yaml:"address"`
	Hostname         string `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	KlippyState      string `json:"klippyState,omitempty" yaml:"klippyState,omitempty"`
	MoonrakerVersion string `json:"moonrakerVersion,omitempty" yaml:"moonrakerVersion,omitempty"`
}

func Scan(
	ctx context.Context,
	cidrs []string,
	port int,
	timeout time.Duration,
	concurrency int,
) ([]Result, error) {
	targets, err := scanAddresses(cidrs)
	if err != nil {
		return nil, err
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if port == 0 {
		port = 7125
	}
	sem := make(chan struct{}, concurrency)
	results := make(chan Result, len(targets))
	var wg sync.WaitGroup
	for _, address := range targets {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			printer := inventory.Printer{
				Name: "discovered", Address: host, Firmware: inventory.FirmwareAuto,
				MoonrakerPort: port,
			}
			client, err := moonraker.New(printer, timeout)
			if err != nil {
				return
			}
			snapshot := client.Snapshot(ctx, printer)
			if !snapshot.Reachable {
				return
			}
			results <- Result{
				Address: host, Hostname: snapshot.Hostname, KlippyState: snapshot.KlippyState,
				MoonrakerVersion: snapshot.MoonrakerVersion,
			}
		}(address)
	}
	wg.Wait()
	close(results)

	var found []Result
	for result := range results {
		found = append(found, result)
	}
	sort.Slice(found, func(i, j int) bool {
		return bytesCompareIP(found[i].Address, found[j].Address) < 0
	})
	return found, ctx.Err()
}

func scanAddresses(cidrs []string) ([]string, error) {
	seen := make(map[string]struct{})
	var out []string
	for _, cidr := range cidrs {
		expanded, err := addresses(cidr)
		if err != nil {
			return nil, err
		}
		for _, address := range expanded {
			if _, duplicate := seen[address]; duplicate {
				continue
			}
			if len(out) >= maxAddresses {
				return nil, fmt.Errorf(
					"combined scan contains more than %d addresses; specify a narrower subnet",
					maxAddresses,
				)
			}
			seen[address] = struct{}{}
			out = append(out, address)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no addresses to scan")
	}
	return out, nil
}

func addresses(cidr string) ([]string, error) {
	ip, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return nil, fmt.Errorf("invalid IPv4 CIDR %q: %w", cidr, err)
	}
	ip = ip.To4()
	if ip == nil {
		return nil, fmt.Errorf("IPv6 discovery is not implemented")
	}
	ones, bits := network.Mask.Size()
	count := 1 << (bits - ones)
	if count > maxAddresses {
		return nil, fmt.Errorf("CIDR contains %d addresses; maximum scan size is %d", count, maxAddresses)
	}
	var out []string
	for candidate := append(net.IP(nil), ip.Mask(network.Mask)...); network.Contains(candidate); increment(candidate) {
		out = append(out, candidate.String())
	}
	if len(out) > 2 {
		out = out[1 : len(out)-1]
	}
	return out, nil
}

// LocalSubnets returns IPv4 prefixes attached to active, non-loopback,
// non-virtual-looking interfaces. Callers may always override this detection by
// passing an explicit CIDR.
func LocalSubnets() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list local network interfaces: %w", err)
	}
	if subnet := routedSubnet(interfaces); subnet != "" {
		return []string{subnet}, nil
	}

	seen := make(map[string]struct{})
	var subnets []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 ||
			iface.Flags&net.FlagLoopback != 0 ||
			iface.Flags&net.FlagPointToPoint != 0 ||
			looksVirtual(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			network, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := network.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			subnet := (&net.IPNet{IP: ip.Mask(network.Mask), Mask: network.Mask}).String()
			if _, duplicate := seen[subnet]; duplicate {
				continue
			}
			seen[subnet] = struct{}{}
			subnets = append(subnets, subnet)
		}
	}
	sort.Strings(subnets)
	if len(subnets) == 0 {
		return nil, fmt.Errorf("no usable local IPv4 subnet found; pass a CIDR explicitly")
	}
	return subnets, nil
}

// routedSubnet maps the kernel's outbound IPv4 route to a local interface prefix.
// UDP dial selects the route without sending a packet, including Hyper-V interfaces.
func routedSubnet(interfaces []net.Interface) string {
	connection, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	local, ok := connection.LocalAddr().(*net.UDPAddr)
	_ = connection.Close()
	if !ok || local.IP == nil {
		return ""
	}
	wanted := local.IP.To4()
	if wanted == nil || wanted.IsLoopback() || wanted.IsLinkLocalUnicast() {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			network, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			address := network.IP.To4()
			if address == nil || !address.Equal(wanted) {
				continue
			}
			return (&net.IPNet{IP: address.Mask(network.Mask), Mask: network.Mask}).String()
		}
	}
	return ""
}

func looksVirtual(name string) bool {
	name = strings.ToLower(name)
	for _, marker := range []string{
		"vethernet", "virtualbox", "vmware", "hyper-v", "docker",
		"podman", "veth", "virbr", "tailscale", "zerotier", "wsl",
	} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return strings.HasPrefix(name, "br-") ||
		strings.HasPrefix(name, "wg") ||
		strings.HasPrefix(name, "utun")
}

func increment(ip net.IP) {
	for index := len(ip) - 1; index >= 0; index-- {
		ip[index]++
		if ip[index] != 0 {
			break
		}
	}
}

func bytesCompareIP(a, b string) int {
	left, right := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}
