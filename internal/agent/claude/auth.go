package claude

// The §9.5 auth probe (task 107), pinned against claude 2.1.268: the fixtures
// in testdata/ are `claude auth status --json` captured logged in and logged
// out, and `claude auth status --help`. The subcommand is official and
// documented, so this is not the state-file parsing v0 T1.7 ruled out —
// vincent reads the command's stdout and never touches `~/.claude*`, the
// keychain or `.credentials.json`.

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
)

// authStatusFloor is the build that introduced `claude auth status`: the
// Claude Code changelog lists "Added `claude auth login`, `claude auth
// status`, and `claude auth logout` CLI subcommands" under 2.1.41.
const authStatusFloor = 41

// authStatusSupported reports whether a detected version is inside
// [2.1.41, 3.0.0) (task 107 decision 2). Outside it nothing is spawned at
// all: a CLI older than the subcommand could read `auth status` as a prompt
// and open an interactive session with no TTY, which hangs until the probe
// timeout on every re-probe. The upper bound is the family supportsInput
// already trusts; a 3.x CLI stays `null` until someone captures its output,
// and `null` is harmless because it is what every claude reported before.
func authStatusSupported(version string) bool {
	parts := strings.SplitN(versionRe.FindString(version), ".", 3)
	if len(parts) != 3 {
		return false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return false
		}
		nums[i] = n
	}
	major, minor, patch := nums[0], nums[1], nums[2]
	if major != 2 || minor < 1 {
		return false
	}
	return minor > 1 || patch >= authStatusFloor
}

// loggedIn probes `claude auth status --json` for the §9.5 field. nil means
// "could not tell". `--json` is passed although it is the default, so a
// future change of default cannot change the format under the parse.
func loggedIn(ctx context.Context, path, version string) *bool {
	if !authStatusSupported(version) {
		return nil
	}
	out, _, err := agent.Probe(ctx, versionTimeout, path, "auth", "status", "--json")
	if ctx.Err() != nil {
		return nil
	}
	return parseAuthStatus(out, err)
}

// authStatus is the whole of what vincent reads from the answer. It has one
// field on purpose (task 107 decision 3): the same JSON carries the account's
// email, organization id and name and subscription type, and none of that is
// decoded, logged, stored or sent over the API. `true` is the CLI's own
// verdict passed through — it means credentials are configured (a claude.ai
// login, an API key, a Bedrock/Vertex/Foundry switch), not that they were
// checked, which is also all codex's `login status` claims.
type authStatus struct {
	LoggedIn *bool `json:"loggedIn"`
}

// parseAuthStatus holds task 107 decision 1: only the JSON field decides.
//
// This is deliberately stricter than the rule codex and cursor share, where a
// non-zero exit is `false`. That leg exists for them because their logged-out
// wording has never been captured. claude's has — logged out is exit 1 *with*
// `{"loggedIn":false,…}` on stdout — and exit 1 is also what every ordinary
// CLI failure returns: an unknown option on some intermediate build, a
// settings deadlock, a config error. Reading those as "not authenticated"
// would be the false accusation T4.22 forbids. So an exit error does not
// short-circuit: stdout is still decoded, and a boolean `loggedIn` is the
// answer whatever the exit code. Everything else is nil.
//
// The timeout leg comes first and is not optional (T4.22): on Windows a probe
// killed by its deadline exits 1, and whatever it half-wrote is not a verdict.
func parseAuthStatus(stdout []byte, err error) *bool {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return nil // could not run it at all
		}
	}
	var st authStatus
	if json.Unmarshal(stdout, &st) != nil {
		return nil
	}
	return st.LoggedIn
}
