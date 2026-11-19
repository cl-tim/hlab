package discover

import "testing"

func TestAddressesDropsNetworkAndBroadcast(t *testing.T) {
	got, err := addresses("192.0.2.0/30")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "192.0.2.1" || got[1] != "192.0.2.2" {
		t.Fatalf("unexpected addresses: %#v", got)
	}
}

func TestAddressesRejectsLargeRange(t *testing.T) {
	if _, err := addresses("198.51.100.0/8"); err == nil {
		t.Fatal("expected range-size error")
	}
}

func TestScanAddressesDeduplicatesOverlappingCIDRs(t *testing.T) {
	got, err := scanAddresses([]string{"192.0.2.0/30", "192.0.2.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "192.0.2.1" || got[1] != "192.0.2.2" {
		t.Fatalf("unexpected addresses: %#v", got)
	}
}

func TestLooksVirtual(t *testing.T) {
	for _, name := range []string{"vEthernet (WSL)", "docker0", "br-example", "tailscale0"} {
		if !looksVirtual(name) {
			t.Errorf("%q should be treated as virtual", name)
		}
	}
	for _, name := range []string{"Ethernet", "Wi-Fi", "enp3s0"} {
		if looksVirtual(name) {
			t.Errorf("%q should be treated as a physical candidate", name)
		}
	}
}
