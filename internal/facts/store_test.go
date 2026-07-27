package facts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	runOne = time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	runTwo = time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "facts.yaml")

	f := Facts{
		Host:          "nas-01",
		UptimeSeconds: 1209600,
		Services:      []Service{{Name: "smbd.service", Description: "Samba"}},
		Ports:         []Port{{Address: "*", Port: 445, Process: "smbd"}},
		Disks:         []Disk{{MountPoint: "/", Type: "ext4", UsePercent: 11}},
	}

	if err := SaveStore(path, Store{"nas-01": SnapshotOf(f, runOne)}); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}

	store, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}

	got, ok := store["nas-01"]
	if !ok {
		t.Fatalf("nas-01 missing from %v", store)
	}
	if !got.CollectedAt.Equal(runOne) {
		t.Errorf("CollectedAt = %v, want %v", got.CollectedAt, runOne)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "smbd.service" {
		t.Errorf("Services = %+v", got.Services)
	}
	if len(got.Ports) != 1 || got.Ports[0].Port != 445 {
		t.Errorf("Ports = %+v", got.Ports)
	}
}

func TestSnapshotExcludesContainers(t *testing.T) {
	// Containers describe what a machine is for, so they live on the host
	// record. Keeping a copy here too would let the two disagree.
	snap := SnapshotOf(Facts{
		Host:       "a",
		Containers: []Container{{Runtime: "docker", Name: "plex", Image: "plexinc/pms-docker"}},
		Services:   []Service{{Name: "docker.service"}},
	}, runOne)

	path := filepath.Join(t.TempDir(), "facts.yaml")
	if err := SaveStore(path, Store{"a": snap}); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}
	raw, _ := os.ReadFile(path)

	for _, unwanted := range []string{"plex", "pms-docker", "containers:"} {
		if strings.Contains(string(raw), unwanted) {
			t.Errorf("the store contains %q, which belongs on the host record:\n%s", unwanted, raw)
		}
	}
}

func TestStoreLeavesOtherHostsAlone(t *testing.T) {
	// Collecting from one machine must not drop what is known about the rest.
	path := filepath.Join(t.TempDir(), "facts.yaml")

	if err := SaveStore(path, Store{
		"a": SnapshotOf(Facts{Host: "a", Services: []Service{{Name: "one.service"}}}, runOne),
		"b": SnapshotOf(Facts{Host: "b", Services: []Service{{Name: "two.service"}}}, runOne),
	}); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}

	// A later run covers only host a.
	if err := SaveStore(path, Store{
		"a": SnapshotOf(Facts{Host: "a", Services: []Service{{Name: "three.service"}}}, runTwo),
	}); err != nil {
		t.Fatalf("second SaveStore: %v", err)
	}

	store, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if len(store) != 2 {
		t.Fatalf("store has %d hosts, want 2: %v", len(store), store)
	}
	if got := store["a"].Services[0].Name; got != "three.service" {
		t.Errorf("a was not refreshed: %q", got)
	}
	if !store["a"].CollectedAt.Equal(runTwo) {
		t.Errorf("a.CollectedAt = %v, want %v", store["a"].CollectedAt, runTwo)
	}
	if got := store["b"].Services[0].Name; got != "two.service" {
		t.Errorf("b was disturbed: %q", got)
	}
	if !store["b"].CollectedAt.Equal(runOne) {
		t.Errorf("b.CollectedAt = %v, want its original run", store["b"].CollectedAt)
	}
}

func TestStoreMissingFileIsEmpty(t *testing.T) {
	store, err := LoadStore(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if len(store) != 0 {
		t.Errorf("store = %v, want empty", store)
	}
}

func TestStoreIsSortedAndLabelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "facts.yaml")

	if err := SaveStore(path, Store{
		"zeta":  SnapshotOf(Facts{Host: "zeta"}, runOne),
		"alpha": SnapshotOf(Facts{Host: "alpha"}, runOne),
	}); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)

	if !strings.Contains(body, "Machine-owned") {
		t.Error("the file does not say it is machine-owned")
	}
	// Stable ordering keeps diffs meaningful across runs.
	if strings.Index(body, "alpha:") > strings.Index(body, "zeta:") {
		t.Errorf("hosts are not sorted:\n%s", body)
	}
}

func TestSnapshotExcludesDurableAttributes(t *testing.T) {
	// Kernel, arch, CPU, memory and network belong on the host record, not in
	// the volatile store. Duplicating them would let the two disagree.
	snap := SnapshotOf(Facts{
		Host:          "a",
		Kernel:        "6.1.0",
		Arch:          "x86_64",
		CPUs:          8,
		UptimeSeconds: 42,
	}, runOne)

	if snap.UptimeSeconds != 42 {
		t.Errorf("UptimeSeconds = %d, want it kept", snap.UptimeSeconds)
	}

	path := filepath.Join(t.TempDir(), "facts.yaml")
	if err := SaveStore(path, Store{"a": snap}); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}
	raw, _ := os.ReadFile(path)

	for _, unwanted := range []string{"kernel", "arch", "cpus", "6.1.0", "x86_64"} {
		if strings.Contains(string(raw), unwanted) {
			t.Errorf("the store contains %q, which belongs on the host record:\n%s", unwanted, raw)
		}
	}
}
