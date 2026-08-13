// Package version exposes build metadata for `vincent version` (spec §12.1).
package version

import (
	"fmt"
	"runtime/debug"
)

// Injected via -ldflags by the mage Build target; plain `go build` binaries
// fall back to VCS metadata from debug.ReadBuildInfo.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// Version returns the version string: the ldflags-injected value when present,
// otherwise the module version stamped into a `go install …@v1.2.3` binary, and
// "dev" for a plain `go build`. The middle case is why this is not a bare
// accessor — `go install` is a documented install path (README), and a binary
// installed that way reporting "dev" would make `vincent version` useless for
// exactly the users who cannot check a release archive's name.
func Version() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		// "(devel)" is what a build from inside the module tree reports; it says
		// no more than "dev" does, so it is not worth preferring.
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return version
}

// Commit returns the abbreviated VCS revision, "unknown" when unavailable.
func Commit() string {
	c, _ := vcsFallback()
	return c
}

// Date returns the build date, "unknown" when unavailable.
func Date() string {
	_, d := vcsFallback()
	return d
}

// String renders one-line build info, e.g.
// "vincent version dev (commit 9ad1de4, built 2026-08-06)".
func String() string {
	return fmt.Sprintf("vincent version %s (commit %s, built %s)", Version(), Commit(), Date())
}

// vcsFallback fills commit/date from debug.ReadBuildInfo when the ldflags
// injection is absent (plain `go build`).
func vcsFallback() (c, d string) {
	c, d = commit, date
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if c == "" {
					c = s.Value
				}
			case "vcs.time":
				if d == "" {
					d = s.Value
				}
			}
		}
	}
	if len(c) > 7 {
		c = c[:7]
	}
	if c == "" {
		c = "unknown"
	}
	if d == "" {
		d = "unknown"
	}
	return c, d
}
