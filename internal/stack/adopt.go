package stack

import (
	"fmt"
	"path"
	"strings"
)

// Markers the adopt script prints so a partial run can be described exactly.
const (
	MarkerStopped = "hlab:stopped"
	MarkerMoved   = "hlab:moved"
	MarkerRolled  = "hlab:rolled-back"
)

// adoptTemplate moves a hand-made project under hlab's ownership.
//
// The directory is moved rather than copied, because a compose file's relative
// bind mounts are the stack's data: ./appdata, ./mainsail/config.json. Copying
// the compose file alone and leaving the data behind produces a stack that
// starts cleanly and is empty, which is the worst of the available failures.
//
// The project keeps its name — either stated in the file, or taken from the
// directory, which is why the destination's base name is the project name.
// That matters more than it looks: compose derives network and volume names
// from the project, so a rename would orphan both.
const adoptTemplate = `
set -eu

SRC=__SRC__
DST=__DST__
ORIG=__ORIG__
CARRY=__CARRY__

if ! command -v docker >/dev/null 2>&1; then
  echo '__M_NO_DOCKER__'
  exit 0
fi
if [ ! -d "$SRC" ]; then
  echo 'hlab:error the project directory is no longer there' >&2
  exit 30
fi
if [ -e "$DST" ]; then
  echo 'hlab:error a stack of that name is already deployed here' >&2
  exit 31
fi

# Stop it where it stands. Containers only: named volumes and the data
# directories beside the compose file are deliberately left alone.
cd "$SRC"
docker compose down --remove-orphans
echo '__M_STOPPED__'

mkdir -p __PARENT__
chmod 0755 __PARENT__

if [ "$CARRY" = 1 ]; then
  # The stack keeps data beside its compose file, so the directory itself is
  # what has to move.
  mv "$SRC" "$DST"
  cd "$DST"
  # The original file is kept until the stack is proven, so a failure has
  # something exact to go back to rather than a reconstruction.
  if [ -f "$ORIG" ]; then
    mv "$ORIG" .hlab-original-compose
  fi
  rm -f docker-compose.yml docker-compose.yaml compose.yml
else
  # Nothing is kept beside the compose file — every mount is absolute or a
  # named volume — so there is nothing to carry and the directory is left
  # exactly where it is. That matters when a project was defined straight in a
  # home directory: moving it would take the home with it.
  mkdir -p "$DST"
  cd "$DST"
fi
echo '__M_MOVED__'

printf '%s' __COMPOSE__ > compose.yaml
chmod 0640 compose.yaml
__ASSETS__
if docker compose up -d --remove-orphans; then
  if [ "$CARRY" = 1 ]; then
    rm -f .hlab-original-compose
  else
    # Rename the original aside. Left in place it is a second, now-stale
    # definition of the same stack that someone can bring up later — which is
    # exactly how a retired service comes back from the dead.
    mv "$SRC/$ORIG" "$SRC/$ORIG.hlab-adopted" 2>/dev/null || true
  fi
  echo '__M_DEPLOYED__'
  docker compose ps --format '{{.Name}}|{{.Service}}|{{.State}}' 2>/dev/null || true
  exit 0
fi

# Put it back exactly as it was: original file, original location, running.
# A half-adopted stack is worse than an unadopted one, and this is the failure
# most likely to happen — a compose file edited before it was deployed.
echo 'hlab:error the stack did not come up; putting it back where it was' >&2
rm -f compose.yaml
if [ "$CARRY" = 1 ]; then
  if [ -f .hlab-original-compose ]; then
    mv .hlab-original-compose "$ORIG"
  fi
  cd /
  mv "$DST" "$SRC"
else
  cd /
  rmdir "$DST" 2>/dev/null || true
fi
cd "$SRC"
docker compose up -d >/dev/null 2>&1 || true
echo '__M_ROLLED__'
exit 32
`

// AdoptOptions describes one project being taken over.
type AdoptOptions struct {
	// Name is the compose project name, and the directory hlab will keep it in.
	Name string
	// SourceDir is where the project lives now.
	SourceDir string
	// OriginalFile is the compose file's current base name, kept for rollback.
	OriginalFile string
	// Compose is the reviewed compose file to deploy in its place.
	Compose []byte
	// Assets are configuration files deployed alongside it.
	Assets []Asset
	// CarriesData says the stack keeps data beside its compose file, so the
	// directory itself has to move. When it does not, the directory is left
	// alone — a project defined straight in a home directory is common, and
	// moving that would take the home with it.
	CarriesData bool
}

// BuildAdoptScript renders the script that relocates a project and redeploys it.
func BuildAdoptScript(o AdoptOptions) (string, error) {
	if err := ValidateName(o.Name); err != nil {
		return "", err
	}
	if len(o.Compose) == 0 {
		return "", fmt.Errorf("compose file is empty")
	}
	if o.SourceDir == "" || !strings.HasPrefix(o.SourceDir, "/") {
		return "", fmt.Errorf("project directory %q is not an absolute path", o.SourceDir)
	}

	dst := RemoteDir + "/" + o.Name
	if o.SourceDir == dst || strings.HasPrefix(o.SourceDir, RemoteDir+"/") {
		return "", fmt.Errorf("%s is already managed by hlab", o.Name)
	}
	// Moving a directory that other things live under would take them too.
	// This only bites when the directory actually moves, but it is checked
	// unconditionally: a plan that would be catastrophic if CarriesData were
	// ever set wrongly should not be constructible at all.
	clean := path.Clean(o.SourceDir)
	switch clean {
	case "/", "/home", "/opt", "/etc", "/var", "/usr", "/root", "/srv":
		return "", fmt.Errorf(
			"refusing to move %s: it is a system directory, not a project directory", clean)
	}
	if o.CarriesData && isHomeDirectory(clean) {
		return "", fmt.Errorf(
			"refusing to move %s: it looks like a home directory, and the stack keeps data beside\n"+
				"its compose file, so adopting it would move the whole home. Put the project in a\n"+
				"directory of its own first, then adopt that", clean)
	}

	original := o.OriginalFile
	if original == "" {
		original = "docker-compose.yml"
	}
	if err := ValidateAssetName(original); err != nil {
		return "", fmt.Errorf("compose file name %q: %w", original, err)
	}

	var b strings.Builder
	seen := map[string]struct{}{}
	for _, asset := range o.Assets {
		if err := ValidateAssetName(asset.Name); err != nil {
			return "", err
		}
		if _, dup := seen[asset.Name]; dup {
			return "", fmt.Errorf("two assets are both named %q", asset.Name)
		}
		seen[asset.Name] = struct{}{}
		fmt.Fprintf(&b, "printf '%%s' %s > %s\n",
			shellQuote(string(asset.Content)), shellQuote(asset.Name))
		fmt.Fprintf(&b, "chmod 0640 %s\n", shellQuote(asset.Name))
	}

	return strings.NewReplacer(
		"__SRC__", shellQuote(o.SourceDir),
		"__DST__", shellQuote(dst),
		"__PARENT__", shellQuote(RemoteDir),
		"__ORIG__", shellQuote(original),
		"__CARRY__", carryFlag(o.CarriesData),
		"__COMPOSE__", shellQuote(string(o.Compose)),
		"__ASSETS__", b.String(),
		"__M_NO_DOCKER__", MarkerNoDocker,
		"__M_STOPPED__", MarkerStopped,
		"__M_MOVED__", MarkerMoved,
		"__M_DEPLOYED__", MarkerDeployed,
		"__M_ROLLED__", MarkerRolled,
	).Replace(adoptTemplate), nil
}

// ProjectName reads the name a compose file declares for itself, if any.
//
// A file that states its name keeps it wherever it is moved to. One that does
// not takes the name from its directory, which is why the destination is named
// after the project rather than anything else.
func ProjectName(compose []byte) string {
	for _, line := range strings.Split(string(compose), "\n") {
		if rest, ok := strings.CutPrefix(line, "name:"); ok {
			return strings.Trim(strings.TrimSpace(rest), `"'`)
		}
	}
	return ""
}

// carryFlag renders CarriesData for the script.
func carryFlag(carries bool) string {
	if carries {
		return "1"
	}
	return "0"
}

// isHomeDirectory reports whether a path is somebody's home rather than a
// directory belonging to one project.
//
// A compose file sitting directly in a home directory is ordinary — it is
// where most people start — and it means the "project directory" docker
// reports is the whole home.
func isHomeDirectory(dir string) bool {
	if dir == "/root" {
		return true
	}
	rest, ok := strings.CutPrefix(dir, "/home/")
	return ok && rest != "" && !strings.Contains(rest, "/")
}

// renameTemplate moves a stack hlab already owns to a new name.
//
// A rename is a relocation of the definition only: the directory moves, the
// compose file is rewritten to state the new name, and the stack is started
// again. Bind-mounted data travels with the directory; named volumes would
// not, which is why the caller refuses the rename when any are declared.
const renameTemplate = `
set -eu

OLD=__OLD__
NEW=__NEW__

if ! command -v docker >/dev/null 2>&1; then
  echo '__M_NO_DOCKER__'
  exit 0
fi
if [ ! -d "$OLD" ]; then
  echo 'hlab:error the stack is not where hlab keeps it' >&2
  exit 60
fi
if [ -e "$NEW" ]; then
  echo 'hlab:error a stack of the new name already exists here' >&2
  exit 61
fi

cd "$OLD"
docker compose down --remove-orphans
echo '__M_STOPPED__'

mv "$OLD" "$NEW"
echo '__M_MOVED__'

cd "$NEW"
printf '%s' __COMPOSE__ > compose.yaml
chmod 0640 compose.yaml

if docker compose up -d --remove-orphans; then
  echo '__M_DEPLOYED__'
  docker compose ps --format '{{.Name}}|{{.Service}}|{{.State}}' 2>/dev/null || true
  exit 0
fi

echo 'hlab:error the stack did not come up under the new name; putting it back' >&2
cd /
mv "$NEW" "$OLD"
cd "$OLD"
docker compose up -d >/dev/null 2>&1 || true
echo '__M_ROLLED__'
exit 62
`

// BuildRenameScript renders the script that renames a managed stack.
func BuildRenameScript(oldName, newName string, compose []byte) (string, error) {
	if err := ValidateName(oldName); err != nil {
		return "", err
	}
	if err := ValidateName(newName); err != nil {
		return "", err
	}
	if oldName == newName {
		return "", fmt.Errorf("%q is already the name", newName)
	}
	if len(compose) == 0 {
		return "", fmt.Errorf("compose file is empty")
	}
	// The file must state the new name, or compose takes it from the directory
	// and the two disagree the moment anything moves again.
	if declared := ProjectName(compose); declared != newName {
		return "", fmt.Errorf(
			"the compose file declares %q; it must say `name: %s` before the rename", declared, newName)
	}

	return strings.NewReplacer(
		"__OLD__", shellQuote(RemoteDir+"/"+oldName),
		"__NEW__", shellQuote(RemoteDir+"/"+newName),
		"__COMPOSE__", shellQuote(string(compose)),
		"__M_NO_DOCKER__", MarkerNoDocker,
		"__M_STOPPED__", MarkerStopped,
		"__M_MOVED__", MarkerMoved,
		"__M_DEPLOYED__", MarkerDeployed,
		"__M_ROLLED__", MarkerRolled,
	).Replace(renameTemplate), nil
}
