package dnssync

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Markers the scripts print.
const (
	MarkerRecord = "hlab:record"
	MarkerDone   = "hlab:dns-done"
	MarkerNoAuth = "hlab:no-admin-password"
)

// PasswordPath is where the Technitium stack keeps its admin password.
const PasswordPath = "/opt/hlab/stacks/technitium/config/admin-password"

// apiPreamble logs in to the local Technitium and leaves a token in $T.
//
// Everything runs on the DNS server itself, against 127.0.0.1, reading the
// password already sitting beside the stack. That is deliberate: hlab needs no
// Technitium credential of its own, nothing is stored on the workstation, and
// no token crosses the network. The machine that owns the secret is the one
// that uses it.
const apiPreamble = `
SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

API=http://127.0.0.1:5380/api
if [ ! -s __PWPATH__ ]; then
  echo '__M_NOAUTH__'
  exit 0
fi
P=$($SUDO cat __PWPATH__)
T=$(curl -s -X POST "$API/user/login" \
  --data-urlencode "user=admin" --data-urlencode "pass=$P" \
  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
if [ -z "$T" ]; then
  echo 'hlab:error could not authenticate to Technitium with the stored password' >&2
  exit 80
fi
`

// Markers separating the two raw API responses the read script returns.
const (
	MarkerJSON  = "hlab:json"
	MarkerScope = "hlab:scope"
)

// BuildReadScript renders the script that dumps a zone's A records.
//
// The raw response comes back and is parsed here rather than picked apart with
// sed on the machine. Technitium nests the address inside an rData object, so
// any line-oriented extraction separates a record's name from its value and
// silently finds nothing — which reads as an empty zone and would have hlab
// recreate every record it already has.
func BuildReadScript(zone string) (string, error) {
	if err := ValidateZone(zone); err != nil {
		return "", err
	}

	return preamble() + `
echo '` + MarkerJSON + `'
curl -s -G "$API/zones/records/get" --data-urlencode "token=$T"   --data-urlencode "domain=` + zone + `" --data-urlencode "zone=` + zone + `"   --data-urlencode "listZone=true"
echo
echo '` + MarkerScope + `'
curl -s -G "$API/dhcp/scopes/get" --data-urlencode "token=$T" --data-urlencode "name=` + zone + `"
echo
echo '` + MarkerDone + `'
`, nil
}

// scopeResponse is the shape of the DHCP scope, for its reserved leases.
type scopeResponse struct {
	Response struct {
		StartingAddress string `json:"startingAddress"`
		EndingAddress   string `json:"endingAddress"`
		ReservedLeases  []struct {
			HardwareAddress string `json:"hardwareAddress"`
			Address         string `json:"address"`
		} `json:"reservedLeases"`
	} `json:"response"`
}

// ParseScopeRange reports the addresses the DHCP scope hands out.
//
// A reservation outside that range means nothing: the server will never offer
// the address, so pinning it is at best noise and at worst an error the
// operator has to interpret. Statically configured machines deliberately live
// outside the pool, which is exactly the case this excludes.
func ParseScopeRange(out string) (start, end string) {
	doc, ok := decodeScope(out)
	if !ok {
		return "", ""
	}
	return doc.Response.StartingAddress, doc.Response.EndingAddress
}

func decodeScope(out string) (scopeResponse, bool) {
	start := strings.Index(out, MarkerScope)
	if start < 0 {
		return scopeResponse{}, false
	}
	body := out[start+len(MarkerScope):]
	if end := strings.Index(body, MarkerDone); end >= 0 {
		body = body[:end]
	}

	var doc scopeResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &doc); err != nil {
		return scopeResponse{}, false
	}
	return doc, true
}

// ParseReservations reads the reserved leases the scope already holds, keyed
// by hardware address.
//
// Without this the plan asserts every reservation on every run and therefore
// always claims there is work to do, which trains an operator to ignore it.
func ParseReservations(out string) map[string]string {
	doc, ok := decodeScope(out)
	if !ok {
		return nil
	}

	out2 := map[string]string{}
	for _, r := range doc.Response.ReservedLeases {
		out2[normaliseMAC(r.HardwareAddress)] = r.Address
	}
	return out2
}

// normaliseMAC puts a hardware address in one form so two spellings of the
// same interface compare equal.
func normaliseMAC(mac string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(mac, "-", ":"), ".", ":"))
}

// recordsResponse is the shape of the zone listing.
type recordsResponse struct {
	Response struct {
		Records []struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			RData struct {
				IPAddress string `json:"ipAddress"`
			} `json:"rData"`
		} `json:"records"`
	} `json:"response"`
	Status string `json:"status"`
}

// ParseRecords reads the A records out of the read script's output.
func ParseRecords(out string) (map[string]string, error) {
	if strings.Contains(out, MarkerNoAuth) {
		return nil, fmt.Errorf(
			"no Technitium admin password at %s, so hlab cannot authenticate.\n"+
				"That file is what `hlab secret` manages", PasswordPath)
	}
	if !strings.Contains(out, MarkerDone) {
		return nil, fmt.Errorf("the zone listing was cut short, so the comparison would be wrong")
	}

	start := strings.Index(out, MarkerJSON)
	if start < 0 {
		return nil, fmt.Errorf("the zone listing produced no response")
	}
	body := out[start+len(MarkerJSON):]
	if end := strings.Index(body, MarkerScope); end >= 0 {
		body = body[:end]
	} else if end := strings.Index(body, MarkerDone); end >= 0 {
		body = body[:end]
	}
	body = strings.TrimSpace(body)

	var doc recordsResponse
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return nil, fmt.Errorf("could not read the zone listing: %w", err)
	}
	if doc.Status != "" && doc.Status != "ok" {
		return nil, fmt.Errorf("the DNS server refused the zone listing (status %q)", doc.Status)
	}

	records := map[string]string{}
	for _, r := range doc.Response.Records {
		if r.Type != "A" || r.RData.IPAddress == "" {
			continue
		}
		records[strings.ToLower(strings.TrimSuffix(r.Name, "."))] = r.RData.IPAddress
	}
	return records, nil
}

// BuildSyncScript renders the script that applies the plan.
//
// Only creates and updates are emitted. Nothing is ever deleted: the zone is
// shared with every device DHCP registers, and a name hlab does not recognise
// is far more likely to be somebody's phone than a mistake.
func BuildSyncScript(zone string, changes []Change, reservations []Reservation) (string, error) {
	if err := ValidateZone(zone); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(preamble())

	for _, c := range changes {
		if c.Action != Create && c.Action != Update {
			continue
		}
		if !validName(c.Name) || !validAddress(c.Address) {
			return "", fmt.Errorf("refusing to write record %q -> %q", c.Name, c.Address)
		}
		fmt.Fprintf(&b, `printf 'record %%s ' %s
curl -s -G "$API/zones/records/add" --data-urlencode "token=$T" `+
			`--data-urlencode "domain=%s" --data-urlencode "zone=%s" --data-urlencode "type=A" `+
			`--data-urlencode "ttl=300" --data-urlencode "ipAddress=%s" --data-urlencode "overwrite=true" `+
			`| sed -n 's/.*"status":"\([^"]*\)".*/\1/p'
`, shellQuote(c.Name), c.Name, zone, c.Address)
	}

	for _, r := range reservations {
		if !validMAC(r.MAC) || !validAddress(r.Address) || !validName(r.Name) {
			return "", fmt.Errorf("refusing to reserve %q for %q", r.Address, r.MAC)
		}
		fmt.Fprintf(&b, `printf 'reservation %%s ' %s
curl -s -G "$API/dhcp/scopes/addReservedLease" --data-urlencode "token=$T" `+
			`--data-urlencode "name=%s" --data-urlencode "hardwareAddress=%s" `+
			`--data-urlencode "ipAddress=%s" --data-urlencode "hostName=%s" `+
			`| sed -n 's/.*"status":"\([^"]*\)".*/\1/p'
`, shellQuote(r.Name), zone, r.MAC, r.Address, r.Name)
	}

	b.WriteString("echo '" + MarkerDone + "'\n")
	return b.String(), nil
}

func preamble() string {
	return strings.NewReplacer(
		"__PWPATH__", shellQuote(PasswordPath),
		"__M_NOAUTH__", MarkerNoAuth,
	).Replace(apiPreamble)
}

// ValidateZone rejects a zone name that is not a zone name.
//
// It is interpolated into a URL inside a root shell script, so it is checked
// rather than trusted.
func ValidateZone(zone string) error {
	if zone == "" {
		return fmt.Errorf("a zone is required")
	}
	for _, label := range strings.Split(zone, ".") {
		if !validLabel(label) {
			return fmt.Errorf("invalid zone %q", zone)
		}
	}
	return nil
}

func validName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !validLabel(label) {
			return false
		}
	}
	return true
}

func validAddress(addr string) bool {
	parts := strings.Split(addr, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func validMAC(mac string) bool {
	parts := strings.Split(mac, ":")
	if len(parts) != 6 {
		return false
	}
	for _, p := range parts {
		if len(p) != 2 {
			return false
		}
		for _, r := range strings.ToLower(p) {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
	}
	return true
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
