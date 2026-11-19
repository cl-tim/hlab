package dnssync

import (
	"strings"
	"testing"
)

const zoneJSON = "hlab:json\n" +
	`{"response":{"records":[` +
	`{"name":"lan","type":"NS","rData":{"nameServer":"technitium.lan"}},` +
	`{"name":"printer-1.lan","type":"A","rData":{"ipAddress":"198.51.100.58"}},` +
	`{"name":"apps-1.lan","type":"A","rData":{"ipAddress":"198.51.100.4"}}]},"status":"ok"}` +
	"\nhlab:scope\n" +
	`{"response":{"startingAddress":"198.51.100.50","endingAddress":"198.51.100.250",` +
	`"reservedLeases":[{"hardwareAddress":"AA-BB-CC-DD-EE-02","address":"198.51.100.100"}]}}` +
	"\nhlab:dns-done\n"

// Technitium nests the address inside an rData object, so any line-oriented
// extraction separates a record's name from its value and quietly finds
// nothing — which reads as an empty zone and would recreate every record.
func TestParseRecordsReadsNestedAddresses(t *testing.T) {
	got, err := ParseRecords(zoneJSON)
	if err != nil {
		t.Fatalf("ParseRecords: %v", err)
	}
	if got["printer-1.lan"] != "198.51.100.58" || got["apps-1.lan"] != "198.51.100.4" {
		t.Errorf("records = %v", got)
	}
	if _, ok := got["lan"]; ok {
		t.Error("a non-A record was included")
	}
}

func TestParseScopeRangeAndReservations(t *testing.T) {
	lo, hi := ParseScopeRange(zoneJSON)
	if lo != "198.51.100.50" || hi != "198.51.100.250" {
		t.Errorf("range = %s-%s", lo, hi)
	}
	// Spellings of a hardware address differ between sources; they must still
	// compare equal or every reservation looks missing.
	if got := ParseReservations(zoneJSON)["aa:bb:cc:dd:ee:02"]; got != "198.51.100.100" {
		t.Errorf("reservation lookup = %q", got)
	}
}

// Comparing against a truncated listing would report every record as missing
// and then recreate the lot.
func TestTruncatedListingIsRefused(t *testing.T) {
	cut := strings.Replace(zoneJSON, "hlab:dns-done", "", 1)
	if _, err := ParseRecords(cut); err == nil {
		t.Error("a truncated zone listing was accepted")
	}
}

func TestMissingPasswordIsExplained(t *testing.T) {
	_, err := ParseRecords(MarkerNoAuth + "\n")
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Errorf("err = %v, want it to point at how the password is managed", err)
	}
}

// The zone reaches a URL inside a root shell script, so it is validated.
func TestZoneIsValidated(t *testing.T) {
	for _, z := range []string{"", "with space", "a'b", "lan;id", "-lan", "l/an"} {
		if err := ValidateZone(z); err == nil {
			t.Errorf("ValidateZone accepted %q", z)
		}
	}
	for _, z := range []string{"lan", "home.arpa", "example-1.lan"} {
		if err := ValidateZone(z); err != nil {
			t.Errorf("ValidateZone rejected %q: %v", z, err)
		}
	}
}

// Nothing may be deleted: the zone is shared with every device DHCP registers.
func TestSyncNeverDeletes(t *testing.T) {
	script, err := BuildSyncScript("lan", []Change{
		{Action: Create, Name: "a.lan", Address: "198.51.100.9"},
		{Action: Extra, Name: "iphone.lan", Address: "198.51.100.62"},
	}, nil)
	if err != nil {
		t.Fatalf("BuildSyncScript: %v", err)
	}
	if strings.Contains(script, "records/delete") || strings.Contains(script, "iphone") {
		t.Error("the sync would remove a record it did not create")
	}
	if !strings.Contains(script, "a.lan") {
		t.Error("the sync does not write the record it should")
	}
}

// Addresses and hardware addresses reach a root shell script.
func TestMalformedValuesAreRefused(t *testing.T) {
	if _, err := BuildSyncScript("lan", []Change{
		{Action: Create, Name: "a.lan", Address: "198.51.100.9; rm -rf /"},
	}, nil); err == nil {
		t.Error("accepted an address that is not an address")
	}
	if _, err := BuildSyncScript("lan", nil, []Reservation{
		{Name: "x", MAC: "not-a-mac", Address: "198.51.100.9"},
	}); err == nil {
		t.Error("accepted a hardware address that is not one")
	}
}
