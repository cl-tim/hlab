package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// scaffoldConfig is written when hlab needs a config file and none exists.
//
// Unlike the `hlab config init` template it contains no example machines: it is
// created as a side effect of ordinary commands, so it must not conjure hosts
// that were never real.
const scaffoldConfig = `# hlab configuration.
#
# Target selectors used by commands like ` + "`hlab run`" + `:
#   all          every host
#   @group       every member of a group
#   #tag         every host carrying a tag
#   name         one host by name (globs work too: "pi-*")
#   !term        subtract, e.g. "all,!#retired"
#
# Run ` + "`hlab config home`" + ` to see where hlab keeps its data.

defaults:
  # Applied to every host that does not override them.
  # user: root
  # port: 22
  # identityFile: ~/.ssh/id_ed25519
  connectTimeout: 10s
  concurrency: 10

# Machines under management. ` + "`hlab enroll`" + ` adds entries here.
hosts: []

# Machines seen by ` + "`hlab discover scan`" + ` but not managed. Maintained by hlab.
discovered: []
`

// EnsureExists creates the config file if it is missing, so no command has to
// be preceded by `hlab config init`. It reports whether it created one.
//
// An existing file is never touched.
func EnsureExists(path string) (bool, error) {
	if path == "" {
		return false, errors.New("no config path given")
	}

	switch _, err := os.Stat(path); {
	case err == nil:
		return false, nil
	case !errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("stat %s: %w", path, err)
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return false, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	// O_EXCL so a config that appeared since the stat is never overwritten.
	// 0600: the inventory records usernames and internal addresses.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("create %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString(scaffoldConfig); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
