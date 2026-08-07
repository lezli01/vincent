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

// String renders one-line build info, e.g.
// "vincent version dev (commit 9ad1de4, built 2026-08-06)".
func String() string {
	v, c, d := version, commit, date
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
	return fmt.Sprintf("vincent version %s (commit %s, built %s)", v, c, d)
}
