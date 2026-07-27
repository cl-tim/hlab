package discover

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

// KnownHosts maps an IP literal to the inventory host name that claims it.
type KnownHosts map[string]string

// BuildKnownHosts resolves inventory addresses down to IP literals so a
// discovered machine is recognised even when the inventory refers to it by DNS
// name. Addresses that fail to resolve are still indexed verbatim, so an
// unresolvable name never causes a managed host to be reported as new.
//
// nameByAddress maps inventory host name -> configured address.
func BuildKnownHosts(ctx context.Context, addressByName map[string]string, timeout time.Duration) KnownHosts {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	known := make(KnownHosts, len(addressByName))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, address := range addressByName {
		// An address that is already an IP needs no lookup.
		if a, err := netip.ParseAddr(address); err == nil {
			known[a.Unmap().String()] = name
			continue
		}

		known[address] = name

		wg.Add(1)
		go func(hostName, addr string) {
			defer wg.Done()

			lookupCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			ips, err := net.DefaultResolver.LookupHost(lookupCtx, addr)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, ip := range ips {
				a, err := netip.ParseAddr(ip)
				if err != nil {
					continue
				}
				// Do not let a resolved name shadow an explicit IP entry.
				if _, exists := known[a.Unmap().String()]; !exists {
					known[a.Unmap().String()] = hostName
				}
			}
		}(name, address)
	}
	wg.Wait()

	return known
}

// Annotate marks results that already appear in the inventory, meaning hlab
// has credentials for them.
func Annotate(results []Result, known KnownHosts) {
	for i := range results {
		if name, ok := known[results[i].Address]; ok {
			results[i].Managed = true
			results[i].ManagedAs = name
			continue
		}
		if results[i].Hostname != "" {
			if name, ok := known[results[i].Hostname]; ok {
				results[i].Managed = true
				results[i].ManagedAs = name
			}
		}
	}
}

// AnnotateSeen marks results that a previous scan already recorded.
func AnnotateSeen(results []Result, seen map[string]bool) {
	for i := range results {
		if seen[results[i].Address] {
			results[i].SeenBefore = true
		}
	}
}

// Unmanaged returns only the results that are not yet in the inventory.
func Unmanaged(results []Result) []Result {
	out := make([]Result, 0, len(results))
	for _, r := range results {
		if !r.Managed {
			out = append(out, r)
		}
	}
	return out
}
