// Package buildinfo carries version metadata stamped in at link time.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// These are overridden via -ldflags at build time. See the Makefile.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// String renders a one-line human readable version banner.
func String() string {
	commit := Commit
	if commit == "" {
		commit = vcsRevision()
	}
	if commit == "" {
		commit = "unknown"
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}

	date := Date
	if date == "" {
		date = "unknown"
	}

	return fmt.Sprintf("hlab %s (commit %s, built %s, %s/%s, %s)",
		Version, commit, date, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

// Info is the machine readable form used by `hlab version -o json`.
type Info struct {
	Version   string `json:"version" yaml:"version"`
	Commit    string `json:"commit" yaml:"commit"`
	BuildDate string `json:"buildDate" yaml:"buildDate"`
	GoVersion string `json:"goVersion" yaml:"goVersion"`
	OS        string `json:"os" yaml:"os"`
	Arch      string `json:"arch" yaml:"arch"`
}

// Current returns the structured build information.
func Current() Info {
	commit := Commit
	if commit == "" {
		commit = vcsRevision()
	}
	return Info{
		Version:   Version,
		Commit:    commit,
		BuildDate: Date,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// vcsRevision recovers the commit from the embedded build info, which `go
// build` populates automatically when building inside a git work tree.
func vcsRevision() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}
