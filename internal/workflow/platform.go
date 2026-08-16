package workflow

import (
	"fmt"
	"runtime"
	"strings"
)

// Platform tokens a workflow's `platforms:` list may carry (spec §8.1.1).
// They are Go's GOOS values, plus one group token: a workflow whose command
// steps lean on `sh`, `cat` and friends says `posix` rather than enumerating
// every Unix vincent might ever run on.
const (
	PlatformLinux   = "linux"
	PlatformDarwin  = "darwin"
	PlatformWindows = "windows"
	// PlatformPosix matches every non-Windows host — the set where a POSIX
	// shell and the usual coreutils are a safe assumption.
	PlatformPosix = "posix"
)

// platformTokens is the accepted set, in the order error messages list it.
var platformTokens = []string{PlatformLinux, PlatformDarwin, PlatformWindows, PlatformPosix}

// HostPlatform is the GOOS of the machine this process runs on. It is the
// platform every restriction is judged against: the daemon is the only thing
// that runs a workflow, and clients speak to it over localhost (§13.1).
func HostPlatform() string { return runtime.GOOS }

// SupportsPlatform reports whether the workflow may run on the given GOOS.
// An empty `platforms:` list means every platform, which is why the field is
// optional and why nothing changes for a workflow that omits it (§8.1.1).
func (w *Workflow) SupportsPlatform(goos string) bool {
	if w == nil || len(w.Platforms) == 0 {
		return true
	}
	for _, p := range w.Platforms {
		if platformMatches(p, goos) {
			return true
		}
	}
	return false
}

// SupportsHost is SupportsPlatform for the host this process runs on.
func (w *Workflow) SupportsHost() bool { return w.SupportsPlatform(HostPlatform()) }

// PlatformSummary renders the restriction for a human: "linux, darwin". It is
// empty for an unrestricted workflow.
func (w *Workflow) PlatformSummary() string {
	if w == nil {
		return ""
	}
	return strings.Join(w.Platforms, ", ")
}

// PlatformMismatch explains why the workflow cannot run on goos, in one
// clause the API's 400 and the engine's failure both embed. It is empty when
// the workflow does run there, so callers branch on the string.
func (w *Workflow) PlatformMismatch(goos string) string {
	if w.SupportsPlatform(goos) {
		return ""
	}
	return fmt.Sprintf("workflow is restricted to %s and this host is %s",
		w.PlatformSummary(), goos)
}

// platformMatches resolves one token against a GOOS. Only `posix` covers more
// than itself; every other token is a GOOS compared literally.
func platformMatches(token, goos string) bool {
	if token == PlatformPosix {
		return goos != PlatformWindows
	}
	return token == goos
}

func isPlatform(s string) bool {
	for _, p := range platformTokens {
		if s == p {
			return true
		}
	}
	return false
}

// validatePlatforms checks the `platforms:` list (§8.1.1, §8.2). Tokens are
// matched exactly, the way every other enum in the schema is: `Linux` or
// `macos` is a typo that fails at load rather than a restriction that silently
// never matched.
func validatePlatforms(wf *Workflow, add func(string, string, ...any)) {
	seen := make(map[string]bool, len(wf.Platforms))
	for i, p := range wf.Platforms {
		path := fmt.Sprintf("platforms[%d]", i)
		switch {
		case !isPlatform(p):
			add(path, "unknown platform %q (one of %s)", p, strings.Join(platformTokens, ", "))
		case seen[p]:
			add(path, "duplicate platform %q", p)
		default:
			seen[p] = true
		}
	}
}
