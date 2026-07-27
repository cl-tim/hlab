package discover

import (
	"context"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// fakeSSH listens on localhost and greets every connection with banner. An
// empty banner means "accept and stay silent", which exercises the timeout.
func fakeSSH(t *testing.T, banner string) netip.AddrPort {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				if banner != "" {
					_, _ = io.WriteString(conn, banner+"\r\n")
				}
				// Hold the connection briefly so the scanner sees a live peer.
				time.Sleep(50 * time.Millisecond)
			}()
		}
	}()

	ap, err := netip.ParseAddrPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("parse listener addr: %v", err)
	}
	return ap
}

// closedPort binds and releases a port so nothing is listening on it.
func closedPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestScanFindsListenerAndReadsBanner(t *testing.T) {
	ap := fakeSSH(t, "SSH-2.0-OpenSSH_9.2p1 Debian-2+deb12u2")

	s := NewScanner(Options{
		Ports:         []int{int(ap.Port())},
		ProbeTimeout:  2 * time.Second,
		BannerTimeout: 2 * time.Second,
	})

	results := s.Scan(context.Background(), []netip.Addr{ap.Addr()})

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Address != "127.0.0.1" {
		t.Errorf("Address = %q, want 127.0.0.1", r.Address)
	}
	if r.Banner != "SSH-2.0-OpenSSH_9.2p1 Debian-2+deb12u2" {
		t.Errorf("Banner = %q", r.Banner)
	}
	if r.Software != "OpenSSH_9.2p1" {
		t.Errorf("Software = %q, want OpenSSH_9.2p1", r.Software)
	}
	if r.OS != "Debian" {
		t.Errorf("OS = %q, want Debian", r.OS)
	}
}

func TestScanIgnoresClosedPorts(t *testing.T) {
	port := closedPort(t)

	s := NewScanner(Options{
		Ports:        []int{port},
		ProbeTimeout: 500 * time.Millisecond,
	})

	results := s.Scan(context.Background(), []netip.Addr{netip.MustParseAddr("127.0.0.1")})
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
}

func TestScanSilentListenerStillReported(t *testing.T) {
	// A port that accepts but never speaks is still a discovery: something is
	// there, we just cannot say what.
	ap := fakeSSH(t, "")

	s := NewScanner(Options{
		Ports:         []int{int(ap.Port())},
		ProbeTimeout:  time.Second,
		BannerTimeout: 200 * time.Millisecond,
	})

	results := s.Scan(context.Background(), []netip.Addr{ap.Addr()})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Banner != "" {
		t.Errorf("Banner = %q, want empty", results[0].Banner)
	}
}

func TestScanMultiplePortsSortedByAddressThenPort(t *testing.T) {
	a := fakeSSH(t, "SSH-2.0-OpenSSH_9.6p1 Ubuntu-3")
	b := fakeSSH(t, "SSH-2.0-OpenSSH_8.4p1 Raspbian-5")

	ports := []int{int(a.Port()), int(b.Port())}
	if ports[0] > ports[1] {
		ports[0], ports[1] = ports[1], ports[0]
	}

	s := NewScanner(Options{Ports: ports, ProbeTimeout: 2 * time.Second, BannerTimeout: time.Second})
	results := s.Scan(context.Background(), []netip.Addr{netip.MustParseAddr("127.0.0.1")})

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Port > results[1].Port {
		t.Errorf("results not sorted by port: %d then %d", results[0].Port, results[1].Port)
	}
}

func TestScanCancellation(t *testing.T) {
	ap := fakeSSH(t, "SSH-2.0-OpenSSH_9.2p1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := NewScanner(Options{Ports: []int{int(ap.Port())}, ProbeTimeout: time.Second})
	results := s.Scan(ctx, []netip.Addr{ap.Addr()})

	if len(results) != 0 {
		t.Errorf("got %d results after cancellation, want 0", len(results))
	}
}

func TestScanEmptyTargets(t *testing.T) {
	s := NewScanner(Options{})
	if got := s.Scan(context.Background(), nil); got != nil {
		t.Errorf("Scan(nil) = %v, want nil", got)
	}
}

func TestScanProgressCallback(t *testing.T) {
	// A silent listener makes every probe wait out the banner timeout, so the
	// scan reliably outlives the progress interval.
	ap := fakeSSH(t, "")

	var (
		mu     sync.Mutex
		called bool
	)

	s := NewScanner(Options{
		Ports:         []int{int(ap.Port())},
		ProbeTimeout:  time.Second,
		BannerTimeout: 50 * time.Millisecond,
		Concurrency:   1,
		ProgressEvery: 10 * time.Millisecond,
		OnProgress: func(int, int, int) {
			mu.Lock()
			called = true
			mu.Unlock()
		},
	})

	// 10 sequential probes at 50ms each is comfortably longer than the 10ms
	// progress interval.
	addrs := make([]netip.Addr, 10)
	for i := range addrs {
		addrs[i] = ap.Addr()
	}
	s.Scan(context.Background(), addrs)

	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Error("OnProgress was never called during a slow scan")
	}
}

func TestParseBanner(t *testing.T) {
	tests := []struct {
		banner       string
		wantSoftware string
		wantOS       string
	}{
		{"SSH-2.0-OpenSSH_9.2p1 Debian-2+deb12u2", "OpenSSH_9.2p1", "Debian"},
		{"SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.5", "OpenSSH_9.6p1", "Ubuntu"},
		{"SSH-2.0-OpenSSH_8.4p1 Raspbian-5+deb11u3", "OpenSSH_8.4p1", "Raspbian"},
		{"SSH-2.0-OpenSSH_9.7", "OpenSSH_9.7", ""},
		{"SSH-2.0-dropbear_2022.83", "dropbear_2022.83", "Dropbear"},
		{"SSH-2.0-OpenSSH_for_Windows_9.5", "OpenSSH_for_Windows_9.5", "Windows"},
		{"SSH-1.99-OpenSSH_3.9p1", "OpenSSH_3.9p1", ""},
		{"", "", ""},
		{"garbage without ssh prefix", "garbage", ""},
	}

	for _, tc := range tests {
		t.Run(tc.banner, func(t *testing.T) {
			software, osHint := ParseBanner(tc.banner)
			if software != tc.wantSoftware {
				t.Errorf("software = %q, want %q", software, tc.wantSoftware)
			}
			if osHint != tc.wantOS {
				t.Errorf("os = %q, want %q", osHint, tc.wantOS)
			}
		})
	}
}

func TestParseBannerPrefersRaspbianOverDebian(t *testing.T) {
	// Raspbian banners mention deb too; the more specific label must win.
	_, osHint := ParseBanner("SSH-2.0-OpenSSH_8.4p1 Raspbian-5+deb11u3")
	if osHint != "Raspbian" {
		t.Errorf("os = %q, want Raspbian", osHint)
	}
}
