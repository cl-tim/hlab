package stack

import (
	"strings"
	"testing"
)

func adoptOpts() AdoptOptions {
	return AdoptOptions{
		Name:         "apps",
		SourceDir:    "/home/alice/apps",
		OriginalFile: "docker-compose.yml",
		Compose:      []byte("name: apps\nservices: {}\n"),
	}
}

// The order is the whole safety property: stop where it stands, move the
// directory with its data, then deploy. A deploy before the move would write
// into a directory the stack is not in.
func TestAdoptStopsMovesThenDeploys(t *testing.T) {
	script, err := BuildAdoptScript(adoptOpts())
	if err != nil {
		t.Fatalf("BuildAdoptScript: %v", err)
	}

	down := strings.Index(script, "docker compose down")
	move := strings.Index(script, `mv "$SRC" "$DST"`)
	up := strings.Index(script, "docker compose up -d --remove-orphans")
	if down < 0 || move < 0 || up < 0 {
		t.Fatalf("script is missing a stage:\n%s", script)
	}
	if !(down < move && move < up) {
		t.Errorf("stages are out of order: down=%d move=%d up=%d", down, move, up)
	}

	// down must not take the data with it.
	if strings.Contains(script, "down --volumes") || strings.Contains(script, "down -v") {
		t.Error("the stack is stopped in a way that destroys its volumes")
	}
}

// A stack that moved but would not start has to end up back where it was,
// running, with its original file — otherwise adopting is a one-way bet.
func TestAdoptRestoresTheOriginalOnFailure(t *testing.T) {
	script, err := BuildAdoptScript(adoptOpts())
	if err != nil {
		t.Fatalf("BuildAdoptScript: %v", err)
	}
	for _, want := range []string{
		".hlab-original-compose",
		`mv "$DST" "$SRC"`,
		MarkerRolled,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("rollback is missing %q", want)
		}
	}
	// The original file is preserved before it can be overwritten.
	if strings.Index(script, "mv \"$ORIG\" .hlab-original-compose") >
		strings.Index(script, "printf '%s' 'name: apps") {
		t.Error("the original compose file is overwritten before it is kept")
	}
}

// SourceDir comes from docker, but it becomes an argument to mv running as
// root. A project whose directory is /home or / would take everything with it.
func TestAdoptRefusesToMoveSystemDirectories(t *testing.T) {
	for _, dir := range []string{"/", "/home", "/opt", "/etc", "/var", "/usr", "/root", "/srv", "/home/"} {
		o := adoptOpts()
		o.SourceDir = dir
		if _, err := BuildAdoptScript(o); err == nil {
			t.Errorf("BuildAdoptScript accepted SourceDir %q", dir)
		}
	}
	o := adoptOpts()
	o.SourceDir = "relative/path"
	if _, err := BuildAdoptScript(o); err == nil {
		t.Error("BuildAdoptScript accepted a relative source directory")
	}
}

// Adopting something hlab already owns would move a directory onto itself.
func TestAdoptRefusesAlreadyManagedProjects(t *testing.T) {
	o := adoptOpts()
	o.SourceDir = RemoteDir + "/apps"
	if _, err := BuildAdoptScript(o); err == nil {
		t.Error("BuildAdoptScript accepted a project it already manages")
	}
}

// The project name decides the network and volume names compose creates, so a
// stack that keeps its name keeps them across the move.
func TestProjectNameIsReadFromTheFile(t *testing.T) {
	if got := ProjectName([]byte("name: apps\nservices: {}\n")); got != "apps" {
		t.Errorf("ProjectName = %q", got)
	}
	if got := ProjectName([]byte(`name: "my-stack"`)); got != "my-stack" {
		t.Errorf("ProjectName = %q", got)
	}
	if got := ProjectName([]byte("services:\n  a:\n    image: x\n")); got != "" {
		t.Errorf("ProjectName = %q, want empty when the file does not say", got)
	}
	// A service called "name" is not the project name.
	if got := ProjectName([]byte("services:\n  name: x\n")); got != "" {
		t.Errorf("ProjectName = %q, want empty", got)
	}
}

func TestAdoptQuotesEverythingItInterpolates(t *testing.T) {
	o := adoptOpts()
	o.SourceDir = "/home/alice/x'; rm -rf / ;'y"
	script, err := BuildAdoptScript(o)
	if err != nil {
		t.Fatalf("BuildAdoptScript: %v", err)
	}
	if strings.Contains(script, "; rm -rf / ;") && !strings.Contains(script, `'\''`) {
		t.Errorf("source directory is not quoted:\n%s", script)
	}
}

// A stack that keeps nothing beside its compose file has nothing to carry, so
// its directory must be left alone. This is not a nicety: docker reports the
// compose file's directory as the project directory, and a compose file
// written straight into /home/alice makes that the "project directory" — moving
// it would relocate the whole home into the stack directory.
func TestAdoptLeavesTheDirectoryAloneWhenThereIsNoDataToCarry(t *testing.T) {
	o := adoptOpts()
	o.SourceDir = "/home/alice"
	o.CarriesData = false

	script, err := BuildAdoptScript(o)
	if err != nil {
		t.Fatalf("BuildAdoptScript: %v", err)
	}
	if !strings.Contains(script, "\nCARRY=0\n") {
		t.Error("the script was not told there is nothing to carry")
	}
	// The move must be conditional, never unguarded.
	move := strings.Index(script, `mv "$SRC" "$DST"`)
	guard := strings.Index(script, `if [ "$CARRY" = 1 ]`)
	if move < 0 || guard < 0 || guard > move {
		t.Error("the directory move is not guarded by CARRY")
	}
	// And the superseded definition is put beyond accidental reuse.
	if !strings.Contains(script, ".hlab-adopted") {
		t.Error("the original compose file is left where it can be brought up again")
	}
}

// Moving a home directory is refused outright, so no combination of inputs can
// produce that plan even if CarriesData were set wrongly.
func TestAdoptRefusesToMoveAHomeDirectory(t *testing.T) {
	for _, dir := range []string{"/home/alice", "/home/alice/", "/root"} {
		o := adoptOpts()
		o.SourceDir = dir
		o.CarriesData = true
		if _, err := BuildAdoptScript(o); err == nil {
			t.Errorf("BuildAdoptScript accepted moving %q", dir)
		}
	}
	// A real project directory under a home is fine.
	o := adoptOpts()
	o.SourceDir = "/home/alice/apps"
	o.CarriesData = true
	if _, err := BuildAdoptScript(o); err != nil {
		t.Errorf("BuildAdoptScript rejected a project directory: %v", err)
	}
}

// A rename must state the new name in the file. Left to the directory, the
// project's identity changes again the next time anything moves.
func TestRenameRequiresTheFileToDeclareTheNewName(t *testing.T) {
	if _, err := BuildRenameScript("old", "new", []byte("name: old\nservices: {}\n")); err == nil {
		t.Error("BuildRenameScript accepted a compose file still declaring the old name")
	}
	if _, err := BuildRenameScript("old", "new", []byte("name: new\nservices: {}\n")); err != nil {
		t.Errorf("BuildRenameScript rejected a correct rename: %v", err)
	}
	if _, err := BuildRenameScript("same", "same", []byte("name: same\n")); err == nil {
		t.Error("BuildRenameScript accepted a rename to the same name")
	}
}

// A rename that fails halfway leaves a stack that exists under neither name.
func TestRenamePutsTheStackBackOnFailure(t *testing.T) {
	script, err := BuildRenameScript("old", "new", []byte("name: new\nservices: {}\n"))
	if err != nil {
		t.Fatalf("BuildRenameScript: %v", err)
	}
	for _, want := range []string{`mv "$NEW" "$OLD"`, MarkerRolled, "docker compose up -d"} {
		if !strings.Contains(script, want) {
			t.Errorf("rename rollback is missing %q", want)
		}
	}
	if !strings.Contains(script, `if [ -e "$NEW" ]`) {
		t.Error("the rename does not check the new name is free first")
	}
}

// Named volumes are prefixed with the project name, so a rename leaves them
// behind and the stack starts against empty ones — which looks like success.
func TestNamedVolumesAreDetected(t *testing.T) {
	compose := []byte("name: x\nservices:\n  a:\n    volumes:\n      - data:/d\nvolumes:\n  data:\n  cache:\n")
	got, err := NamedVolumes(compose)
	if err != nil {
		t.Fatalf("NamedVolumes: %v", err)
	}
	if strings.Join(got, ",") != "cache,data" {
		t.Errorf("NamedVolumes = %v, want [cache data]", got)
	}

	none, err := NamedVolumes([]byte("name: x\nservices:\n  a:\n    volumes:\n      - ./d:/d\n"))
	if err != nil {
		t.Fatalf("NamedVolumes: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("NamedVolumes = %v, want none for bind mounts only", none)
	}
}
