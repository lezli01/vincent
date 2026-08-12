package daemon

import (
	"log/slog"
	"strings"

	"github.com/lezli01/vincent/internal/config"
)

// logEnvironment reports the §12.3 environment child processes will inherit
// (T4.23).
//
// The done-when has two halves and this is the second: a policy that is
// configurable but unreported would leave the next environmental failure
// exactly as undiagnosable as the first one was. It took two wrong diagnoses
// to find that MSYSTEM was the trigger, and a line naming it would have ended
// that in one look — which is why the full name list is at info rather than
// behind debug, and why a count would not do.
//
// **Names only, never values.** An environment block is where every agent
// CLI's credentials live, and a daemon log is something people paste into
// issues. The same reasoning already keeps `debug:` off by default because
// argv can carry a prompt.
//
// It returns the names it logged so a reload can compare and stay quiet when
// nothing moved.
func logEnvironment(logger *slog.Logger, env config.Environment) []string {
	resolved := env.ResolveProcess()
	names := config.Names(resolved)
	logger.Info("child environment resolved",
		"inherit", env.Inherit.String(),
		"unset", len(env.Unset),
		"set", len(env.Set),
		"count", len(names),
		"names", strings.Join(names, ","))

	// Honored as written — a hermetic environment with an absolute-path
	// toolchain is a legitimate thing to ask for — so a missing load-bearing
	// variable is announced, not corrected. Announcing matters because the
	// failure is otherwise both silent and late: adapters are resolved with
	// exec.LookPath in this process and started by absolute path, so an agent
	// with no PATH starts perfectly and fails three steps in, when the CLI
	// shells out to git.
	for _, missing := range config.LoadBearing(resolved) {
		logger.Warn("child environment omits a variable children need",
			"name", missing,
			"effect", "agent steps start but commands they shell out to will fail",
			"fix", "add it to environment.inherit, or give it a value under environment.set")
	}
	return names
}

// sameNames reports whether two resolved name lists match. Both are sorted by
// config.Names, so this is an element-wise compare rather than a set compare.
func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
