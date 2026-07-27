package stack

import (
	"strings"
	"testing"
)

func TestPublishedPortsAcrossComposeForms(t *testing.T) {
	compose := []byte(`
services:
  dns:
    image: technitium/dns-server
    ports:
      - "53:53/udp"
      - "53:53/tcp"
      - "0.0.0.0:5380:5380"
      - target: 53443
        published: 53443
        protocol: tcp
  web:
    image: caddy
    ports:
      - "80:80"
      - "8443"
`)

	got, err := PublishedPorts(compose)
	if err != nil {
		t.Fatalf("PublishedPorts: %v", err)
	}

	var ports []int
	for _, p := range got {
		ports = append(ports, p.Port)
	}

	// 53 appears twice (udp and tcp), 8443 alone publishes a random host port
	// and claims nothing.
	want := []int{53, 53, 80, 5380, 53443}
	if len(ports) != len(want) {
		t.Fatalf("ports = %v, want %v", ports, want)
	}
	for i := range want {
		if ports[i] != want[i] {
			t.Fatalf("ports = %v, want %v", ports, want)
		}
	}
}

func TestPublishedPortsIgnoresRanges(t *testing.T) {
	// Treating "8000-8010:8000-8010" as a claim on 8000 alone would produce a
	// conflict that may not be real; better to claim nothing than to lie.
	got, err := PublishedPorts([]byte("services:\n  a:\n    ports: [\"8000-8010:8000-8010\"]\n"))
	if err != nil {
		t.Fatalf("PublishedPorts: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("PublishedPorts = %+v, want nothing claimed for a range", got)
	}
}

func TestPublishedPortsHostNetworkingClaimsNothing(t *testing.T) {
	// With host networking there is no published-port mapping to inspect, so
	// the check has nothing to say — it must not invent a conflict.
	got, err := PublishedPorts([]byte("services:\n  dns:\n    network_mode: host\n"))
	if err != nil {
		t.Fatalf("PublishedPorts: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("PublishedPorts = %+v, want none", got)
	}
}

func TestValidateNameRejectsPathEscapes(t *testing.T) {
	// The name becomes a directory under the stack root, so anything that
	// could climb out of it is refused.
	for _, bad := range []string{"", "../etc", "a/b", "Stack", "a b", "a;rm -rf /", "a$(id)"} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", bad)
		}
	}
	for _, good := range []string{"technitium", "caddy-proxy", "dns_1"} {
		if err := ValidateName(good); err != nil {
			t.Errorf("ValidateName(%q) = %v", good, err)
		}
	}
}

func TestBuildDeployScriptChecksDockerFirst(t *testing.T) {
	script, err := BuildDeployScript("technitium", []byte("services: {}\n"), nil)
	if err != nil {
		t.Fatalf("BuildDeployScript: %v", err)
	}

	docker := strings.Index(script, "command -v docker")
	write := strings.Index(script, "> \"$DIR/compose.yaml\"")
	if docker < 0 || write < 0 {
		t.Fatal("script does not both check for docker and write the compose file")
	}
	if docker > write {
		t.Error("the compose file is written before docker is checked for")
	}
	if !strings.Contains(script, "docker compose up -d") {
		t.Error("script does not bring the stack up")
	}
	if !strings.Contains(script, RemoteDir+"/technitium") {
		t.Error("script does not target the named stack directory")
	}
}

func TestBuildDeployScriptQuotesCompose(t *testing.T) {
	// A compose file is operator-supplied text written through a shell; a
	// stray quote must not become shell syntax.
	compose := []byte("services:\n  a:\n    command: echo 'it''s fine'\n")
	script, err := BuildDeployScript("t", compose, nil)
	if err != nil {
		t.Fatalf("BuildDeployScript: %v", err)
	}
	if strings.Contains(script, "\n echo 'it's") {
		t.Errorf("compose content escaped its quoting:\n%s", script)
	}
	if !strings.Contains(script, `'\''`) {
		t.Error("single quotes in the compose file were not escaped")
	}
}

func TestBuildDeployScriptRejectsEmpty(t *testing.T) {
	if _, err := BuildDeployScript("t", nil, nil); err == nil {
		t.Error("an empty compose file was accepted")
	}
}

func TestParseContainerStates(t *testing.T) {
	got := ParseContainerStates(
		"hlab:deployed\ntechnitium|technitium|running\nzz|other|exited\nrubbish\n")
	if len(got) != 2 {
		t.Fatalf("states = %+v, want 2", got)
	}
	if got[0].Name != "technitium" || got[0].State != "running" {
		t.Errorf("states[0] = %+v", got[0])
	}
}

// A stack whose configuration is a separate file — Caddy is the case that
// prompted this — is only reproducible if that file travels with it.
func TestDeployWritesAssetsAlongsideTheComposeFile(t *testing.T) {
	script, err := BuildDeployScript("caddy", []byte("services: {}\n"), []Asset{
		{Name: "Caddyfile", Content: []byte("example.example {\n\treverse_proxy app:80\n}\n")},
	})
	if err != nil {
		t.Fatalf("BuildDeployScript: %v", err)
	}

	if !strings.Contains(script, `> "$DIR"/'Caddyfile'`) {
		t.Errorf("asset is not written into the stack directory:\n%s", script)
	}
	if !strings.Contains(script, "reverse_proxy app:80") {
		t.Error("asset content is missing")
	}

	// It has to land before compose runs, or the first up mounts nothing.
	if strings.Index(script, "Caddyfile") > strings.Index(script, "docker compose up") {
		t.Error("assets are written after the stack is started")
	}
}

// The asset name becomes a path on a remote machine in a script running as
// root, so it is a security boundary rather than a convenience.
func TestAssetNamesCannotEscapeTheStackDirectory(t *testing.T) {
	for _, name := range []string{
		"../../etc/cron.d/x", "/etc/passwd", "..", ".", "", "-rf",
		`sub\dir`, "compose.yaml",
	} {
		if err := ValidateAssetName(name); err == nil {
			t.Errorf("ValidateAssetName(%q) = nil, want an error", name)
		}
	}
	for _, name := range []string{"Caddyfile", "rules.yaml", "50-custom.conf"} {
		if err := ValidateAssetName(name); err != nil {
			t.Errorf("ValidateAssetName(%q) = %v, want nil", name, err)
		}
	}
}

// Two assets with the same name would silently leave whichever was written
// last, which is not what the caller asked for either way.
func TestDuplicateAssetNamesAreRefused(t *testing.T) {
	_, err := BuildDeployScript("s", []byte("services: {}\n"), []Asset{
		{Name: "Caddyfile", Content: []byte("a")},
		{Name: "Caddyfile", Content: []byte("b")},
	})
	if err == nil {
		t.Error("BuildDeployScript accepted two assets with the same name")
	}
}

// Asset content is attacker-adjacent in the sense that it is arbitrary text
// being interpolated into a shell script; a quote in it must not end the word.
func TestAssetContentCannotBreakOutOfTheScript(t *testing.T) {
	script, err := BuildDeployScript("s", []byte("services: {}\n"), []Asset{
		{Name: "Caddyfile", Content: []byte("x' ; rm -rf / ; echo '")},
	})
	if err != nil {
		t.Fatalf("BuildDeployScript: %v", err)
	}
	if strings.Contains(script, "; rm -rf / ;") && !strings.Contains(script, `'\''`) {
		t.Errorf("asset content is not quoted:\n%s", script)
	}
}
