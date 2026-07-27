package discover

import (
	"net/netip"
	"strings"
	"testing"
)

func addrStrings(addrs []netip.Addr) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

func TestExpandTargets(t *testing.T) {
	tests := []struct {
		name  string
		specs []string
		want  []string
	}{
		{
			name:  "single address",
			specs: []string{"192.0.2.10"},
			want:  []string{"192.0.2.10"},
		},
		{
			name:  "slash 30 skips network and broadcast",
			specs: []string{"192.0.2.0/30"},
			want:  []string{"192.0.2.1", "192.0.2.2"},
		},
		{
			name:  "slash 31 keeps both ends",
			specs: []string{"192.0.2.0/31"},
			want:  []string{"192.0.2.0", "192.0.2.1"},
		},
		{
			name:  "slash 32 is a single host",
			specs: []string{"192.0.2.7/32"},
			want:  []string{"192.0.2.7"},
		},
		{
			name:  "unmasked cidr is normalised",
			specs: []string{"192.0.2.37/30"},
			want:  []string{"192.0.2.37", "192.0.2.38"},
		},
		{
			name:  "explicit range",
			specs: []string{"198.51.100.5-198.51.100.8"},
			want:  []string{"198.51.100.5", "198.51.100.6", "198.51.100.7", "198.51.100.8"},
		},
		{
			name:  "last octet shorthand",
			specs: []string{"198.51.100.5-7"},
			want:  []string{"198.51.100.5", "198.51.100.6", "198.51.100.7"},
		},
		{
			name:  "single address range",
			specs: []string{"198.51.100.5-5"},
			want:  []string{"198.51.100.5"},
		},
		{
			name:  "range crossing an octet boundary",
			specs: []string{"198.51.100.254-198.51.101.1"},
			want:  []string{"198.51.100.254", "198.51.100.255", "198.51.101.0", "198.51.101.1"},
		},
		{
			name:  "multiple specs are unioned and deduped",
			specs: []string{"198.51.100.1", "198.51.100.1", "198.51.100.2"},
			want:  []string{"198.51.100.1", "198.51.100.2"},
		},
		{
			name:  "ipv6 single address",
			specs: []string{"fd00::1"},
			want:  []string{"fd00::1"},
		},
		{
			name:  "ipv6 prefix keeps every address",
			specs: []string{"fd00::/126"},
			want:  []string{"fd00::", "fd00::1", "fd00::2", "fd00::3"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandTargets(tc.specs, 0)
			if err != nil {
				t.Fatalf("ExpandTargets(%v): %v", tc.specs, err)
			}
			gotS := addrStrings(got)
			if strings.Join(gotS, ",") != strings.Join(tc.want, ",") {
				t.Errorf("ExpandTargets(%v) = %v, want %v", tc.specs, gotS, tc.want)
			}
		})
	}
}

func TestExpandTargetsSlash24Size(t *testing.T) {
	got, err := ExpandTargets([]string{"192.0.2.0/24"}, 0)
	if err != nil {
		t.Fatalf("ExpandTargets: %v", err)
	}
	if len(got) != 254 {
		t.Fatalf("got %d addresses, want 254", len(got))
	}
	if got[0].String() != "192.0.2.1" {
		t.Errorf("first = %s, want 192.0.2.1", got[0])
	}
	if last := got[len(got)-1].String(); last != "192.0.2.254" {
		t.Errorf("last = %s, want 192.0.2.254", last)
	}
}

func TestExpandTargetsErrors(t *testing.T) {
	tests := []struct {
		name  string
		specs []string
		max   int
	}{
		{"empty list", nil, 0},
		{"blank spec", []string{"   "}, 0},
		{"garbage", []string{"not-an-ip"}, 0},
		{"bad cidr", []string{"192.0.2.0/33"}, 0},
		{"reversed range", []string{"198.51.100.9-198.51.100.2"}, 0},
		{"bad octet shorthand", []string{"198.51.100.5-999"}, 0},
		{"mixed family range", []string{"198.51.100.1-fd00::5"}, 0},
		{"ipv6 shorthand unsupported", []string{"fd00::1-5"}, 0},
		{"exceeds max", []string{"198.51.100.0/16"}, 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ExpandTargets(tc.specs, tc.max); err == nil {
				t.Errorf("ExpandTargets(%v) = %d addresses, want error", tc.specs, len(got))
			}
		})
	}
}

func TestExpandTargetsMaxMessageIsActionable(t *testing.T) {
	_, err := ExpandTargets([]string{"198.51.100.0/8"}, 1024)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "--max-targets") {
		t.Errorf("error = %q, want it to name the flag that raises the cap", err)
	}
}

func TestLastAddr4(t *testing.T) {
	tests := []struct{ prefix, want string }{
		{"192.0.2.0/24", "192.0.2.255"},
		{"192.0.2.0/30", "192.0.2.3"},
		{"198.51.100.0/8", "198.255.255.255"},
		{"0.0.0.0/0", "255.255.255.255"},
		{"192.0.2.7/32", "192.0.2.7"},
	}

	for _, tc := range tests {
		p, err := netip.ParsePrefix(tc.prefix)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.prefix, err)
		}
		if got := lastAddr4(p).String(); got != tc.want {
			t.Errorf("lastAddr4(%s) = %s, want %s", tc.prefix, got, tc.want)
		}
	}
}
