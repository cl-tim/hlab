package stack

import (
	"strings"
	"testing"
)

const composeLsOutput = `[{"Name":"homarr","Status":"created(1), running(1)","ConfigFiles":"/home/alice/homarr/docker-compose.yml"},{"Name":"technitium","Status":"running(1)","ConfigFiles":"/opt/hlab/stacks/technitium/compose.yaml"}]`

func TestParseProjectsReadsDockersListing(t *testing.T) {
	projects, err := ParseProjects(composeLsOutput)
	if err != nil {
		t.Fatalf("ParseProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	if projects[0].Name != "homarr" || projects[0].Dir() != "/home/alice/homarr" {
		t.Errorf("project[0] = %+v", projects[0])
	}
	if projects[0].Adopted() {
		t.Error("a hand-made project under /home is reported as hlab's")
	}
	if !projects[1].Adopted() {
		t.Error("a project under the stack directory is not recognised as hlab's")
	}
}

// A sudo lecture or a deprecation notice ahead of the JSON is ordinary, and
// would otherwise make a machine full of stacks look empty.
func TestParseProjectsSkipsNoiseBeforeTheJSON(t *testing.T) {
	noisy := "sudo: unable to resolve host pi\nWARNING: some deprecation\n" + composeLsOutput
	projects, err := ParseProjects(noisy)
	if err != nil {
		t.Fatalf("ParseProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("got %d projects, want 2", len(projects))
	}
}

func TestParseFilesSplitsOnTheMarker(t *testing.T) {
	out := FileMarker + "/a/compose.yml\nname: a\nservices: {}\n" +
		FileMarker + "/b/compose.yml\nname: b\n"

	files := ParseFiles(out)
	if got := files["/a/compose.yml"]; got != "name: a\nservices: {}" {
		t.Errorf("file a = %q", got)
	}
	if got := files["/b/compose.yml"]; got != "name: b" {
		t.Errorf("file b = %q", got)
	}
}

// The compose file alone is not the stack. Adopting a project means moving the
// data it keeps beside itself, and a relocation that misses these comes up
// empty — homarr keeps its database in ./appdata.
func TestRelativeBindMountsFindTheDataAStackKeepsBesideItself(t *testing.T) {
	compose := []byte(`
name: homarr
services:
  homarr:
    volumes:
      - ./appdata:/appdata
      - /var/run/docker.sock:/var/run/docker.sock
  other:
    volumes:
      - type: bind
        source: ./etc-pihole
        target: /etc/pihole
      - namedvol:/data
volumes:
  namedvol:
`)

	mounts, err := RelativeBindMounts(compose)
	if err != nil {
		t.Fatalf("RelativeBindMounts: %v", err)
	}

	var got []string
	for _, m := range mounts {
		got = append(got, m.Source)
	}
	want := "./appdata ./etc-pihole"
	if strings.Join(got, " ") != want {
		t.Errorf("sources = %v, want %s", got, want)
	}
}

// An absolute source and a named volume do not move with the compose file, so
// reporting them as data to relocate would send an operator chasing nothing.
func TestRelativeBindMountsIgnoreWhatDoesNotMove(t *testing.T) {
	compose := []byte("services:\n  s:\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n      - caddy_data:/data\n      - /data\n")
	mounts, err := RelativeBindMounts(compose)
	if err != nil {
		t.Fatalf("RelativeBindMounts: %v", err)
	}
	if len(mounts) != 0 {
		t.Errorf("mounts = %+v, want none", mounts)
	}
}

func TestBuildReadScriptQuotesPaths(t *testing.T) {
	script := BuildReadScript([]string{"/home/alice/x'; rm -rf / ;'/compose.yml"})
	if strings.Contains(script, "; rm -rf / ;") && !strings.Contains(script, `'\''`) {
		t.Errorf("path is not quoted:\n%s", script)
	}
	if !strings.Contains(script, FileMarker) {
		t.Error("script does not emit the file marker")
	}
}
