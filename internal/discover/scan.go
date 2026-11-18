package discover

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Result is one reachable address:port found by a scan.
type Result struct {
	Address   string `json:"address" yaml:"address"`
	Port      int    `json:"port" yaml:"port"`
	Hostname  string `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	Banner    string `json:"banner,omitempty" yaml:"banner,omitempty"`
	Software  string `json:"software,omitempty" yaml:"software,omitempty"`
	OS        string `json:"os,omitempty" yaml:"os,omitempty"`
	LatencyMs int64  `json:"latencyMs" yaml:"latencyMs"`
	// Managed is set when the address already appears in the inventory, i.e.
	// hlab has credentials for it.
	Managed   bool   `json:"managed" yaml:"managed"`
	ManagedAs string `json:"managedAs,omitempty" yaml:"managedAs,omitempty"`
	Ignored   bool   `json:"ignored,omitempty" yaml:"ignored,omitempty"`
	// SeenBefore is set when a previous scan already recorded this machine.
	// It is independent of Managed: a machine can be seen repeatedly and still
	// never be brought under management.
	SeenBefore bool `json:"seenBefore" yaml:"seenBefore"`
	// Change is how this sighting compared to the stored record: "new",
	// "changed" or "unchanged". Empty when the scan was not recorded.
	Change string `json:"change,omitempty" yaml:"change,omitempty"`
	// Changes lists what differed, e.g. "ssh: OpenSSH_9.6p1 -> OpenSSH_10.0p2".
	Changes []string `json:"changes,omitempty" yaml:"changes,omitempty"`

	addr netip.Addr
}

// Options configure a scan.
type Options struct {
	// Ports to probe on every address. Defaults to 22.
	Ports []int
	// ProbeTimeout bounds each TCP connect. Dead addresses cost this much, so
	// on a LAN it should stay short.
	ProbeTimeout time.Duration
	// BannerTimeout bounds the wait for an SSH identification string. It only
	// applies to addresses that already accepted a connection.
	BannerTimeout time.Duration
	// Concurrency is how many probes run at once.
	Concurrency int
	// ResolveNames enables reverse DNS for every hit.
	ResolveNames bool
	// OnProgress, if set, is called periodically during long scans.
	OnProgress func(scanned, total, found int)
	// ProgressEvery controls how often OnProgress fires. Defaults to 3s.
	ProgressEvery time.Duration
}

func (o *Options) applyDefaults() {
	if len(o.Ports) == 0 {
		o.Ports = []int{22}
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = time.Second
	}
	if o.BannerTimeout <= 0 {
		o.BannerTimeout = 2 * time.Second
	}
	if o.Concurrency < 1 {
		o.Concurrency = 256
	}
	if o.ProgressEvery <= 0 {
		o.ProgressEvery = 3 * time.Second
	}
}

// Scanner probes a set of addresses for reachable TCP ports.
type Scanner struct {
	opts Options
}

// NewScanner returns a Scanner with defaults filled in for any unset option.
func NewScanner(opts Options) *Scanner {
	opts.applyDefaults()
	return &Scanner{opts: opts}
}

type job struct {
	addr netip.Addr
	port int
}

// Scan probes every address on every configured port and returns the hits,
// sorted by address then port. Cancelling ctx stops the scan early and returns
// whatever was found so far.
func (s *Scanner) Scan(ctx context.Context, addrs []netip.Addr) []Result {
	total := len(addrs) * len(s.opts.Ports)
	if total == 0 {
		return nil
	}

	jobs := make(chan job)
	var (
		mu      sync.Mutex
		results []Result

		scanned atomic.Int64
		found   atomic.Int64
	)

	// Progress reporting runs off the workers so a slow printer cannot stall
	// the scan.
	if s.opts.OnProgress != nil {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(s.opts.ProgressEvery)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					s.opts.OnProgress(int(scanned.Load()), total, int(found.Load()))
				case <-stop:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	workers := s.opts.Concurrency
	if workers > total {
		workers = total
	}

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					return
				}
				r := s.probe(ctx, j.addr, j.port)
				scanned.Add(1)
				if r == nil {
					continue
				}
				found.Add(1)
				mu.Lock()
				results = append(results, *r)
				mu.Unlock()
			}
		}()
	}

	// Feed jobs port-major so a single-port scan walks the range in order.
feed:
	for _, port := range s.opts.Ports {
		for _, a := range addrs {
			select {
			case jobs <- job{addr: a, port: port}:
			case <-ctx.Done():
				break feed
			}
		}
	}
	close(jobs)
	wg.Wait()

	if s.opts.ResolveNames {
		s.resolveNames(ctx, results)
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].addr != results[j].addr {
			return results[i].addr.Less(results[j].addr)
		}
		return results[i].Port < results[j].Port
	})
	return results
}

// probe opens a TCP connection and, if it succeeds, reads the SSH banner.
// A nil return means nothing is listening.
func (s *Scanner) probe(ctx context.Context, a netip.Addr, port int) *Result {
	dialCtx, cancel := context.WithTimeout(ctx, s.opts.ProbeTimeout)
	defer cancel()

	target := netip.AddrPortFrom(a, uint16(port)).String()

	start := time.Now()
	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "tcp", target)
	if err != nil {
		return nil
	}
	defer func() { _ = conn.Close() }()
	latency := time.Since(start)

	banner := readBanner(conn, s.opts.BannerTimeout)
	software, osHint := ParseBanner(banner)

	return &Result{
		Address:   a.String(),
		Port:      port,
		Banner:    banner,
		Software:  software,
		OS:        osHint,
		LatencyMs: latency.Milliseconds(),
		addr:      a,
	}
}

// readBanner reads the server's identification string. RFC 4253 allows a
// server to send arbitrary lines before the SSH- line, so a few are tolerated.
func readBanner(conn net.Conn, timeout time.Duration) string {
	if timeout <= 0 {
		return ""
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))

	// Cap the read so a chatty or hostile service cannot stream forever.
	r := bufio.NewReader(io.LimitReader(conn, 2048))

	var first string
	for range 4 {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if first == "" && line != "" {
			first = line
		}
		if strings.HasPrefix(line, "SSH-") {
			return line
		}
		if err != nil {
			break
		}
	}
	return first
}

// osDetect maps a substring of the banner to a human readable OS label. Order
// matters: the more specific entries come first.
var osDetect = []struct{ needle, label string }{
	{"raspbian", "Raspbian"},
	{"debian", "Debian"},
	{"ubuntu", "Ubuntu"},
	{"alpine", "Alpine"},
	{"freebsd", "FreeBSD"},
	{"openbsd", "OpenBSD"},
	{"netbsd", "NetBSD"},
	{"centos", "CentOS"},
	{"rocky", "Rocky"},
	{"almalinux", "AlmaLinux"},
	{"fedora", "Fedora"},
	{"red hat", "RHEL"},
	{"rhel", "RHEL"},
	{"suse", "SUSE"},
	{"arch", "Arch"},
	{"gentoo", "Gentoo"},
	{"synology", "Synology"},
	{"truenas", "TrueNAS"},
	{"unifi", "UniFi"},
	{"mikrotik", "MikroTik"},
	{"routeros", "RouterOS"},
	{"dropbear", "Dropbear"},
	{"windows", "Windows"},
	{"proxmox", "Proxmox"},
}

// ParseBanner extracts the software version and an OS hint from an SSH
// identification string such as:
//
//	SSH-2.0-OpenSSH_9.2p1 Debian-2+deb12u2
func ParseBanner(banner string) (software, osHint string) {
	if banner == "" {
		return "", ""
	}

	rest := banner
	if i := strings.Index(rest, "SSH-"); i >= 0 {
		rest = rest[i:]
		// SSH-<protoversion>-<softwareversion>[ <comments>]
		if parts := strings.SplitN(rest, "-", 3); len(parts) == 3 {
			rest = parts[2]
		}
	}

	software = rest
	if i := strings.IndexByte(rest, ' '); i >= 0 {
		software = rest[:i]
	}

	lower := strings.ToLower(banner)
	for _, d := range osDetect {
		if strings.Contains(lower, d.needle) {
			osHint = d.label
			break
		}
	}
	return software, osHint
}

// resolveNames fills in Hostname via reverse DNS, in parallel and best effort.
func (s *Scanner) resolveNames(ctx context.Context, results []Result) {
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup

	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()

			names, err := net.DefaultResolver.LookupAddr(lookupCtx, results[idx].Address)
			if err != nil || len(names) == 0 {
				return
			}
			results[idx].Hostname = strings.TrimSuffix(names[0], ".")
		}(i)
	}
	wg.Wait()
}
