package skill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// Repo is the published source `skills add` fetches from. It is the same
// string README.md and docs/guides/workflows.md print by hand, and keeping
// the two spellings in step is what installArgvTest asserts (decision 1).
const Repo = "lezli01/vincent"

// installer is the CLI vincent shells out to. It is `npx` and not a resolved
// path so the missing-dependency message names what a user would install;
// exec.LookPath finds `npx.cmd` through PATHEXT on Windows without this
// having to know that.
const installer = "npx"

// ErrNoInstaller is `npx` not being on PATH. It is a first-class outcome
// rather than a crash: detection does not need node, so a machine without it
// still gets a complete report and one actionable sentence (decision 1).
var ErrNoInstaller = errors.New("npx is not installed")

// agentSlug maps a vincent adapter (§9.5) to the `skills --agent` slug and to
// the global directory that CLI links into. The mapping is a table and not an
// identity: claude's slug is `claude-code`.
type agentSlug struct {
	Adapter string
	Slug    string
	Root    string
}

// agents is that table, in the order the argv lists them. Only vincent's
// three adapters are here — this decides what vincent *installs for*, not
// what it *reports*: detection scans the home directory, so an agent vincent
// does not drive still appears in a row when it holds a link.
var agents = []agentSlug{
	{Adapter: "claude", Slug: "claude-code", Root: ".claude"},
	{Adapter: "codex", Slug: "codex", Root: ".codex"},
	{Adapter: "cursor", Slug: "cursor", Root: ".cursor"},
}

// Adapters lists the vincent adapters skills can be installed for.
func Adapters() []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.Adapter)
	}
	return out
}

// adapterForRoot names the vincent adapter a home directory belongs to, empty
// for the agents vincent does not drive.
func adapterForRoot(root string) string {
	for _, a := range agents {
		if a.Root == root {
			return a.Adapter
		}
	}
	return ""
}

// Slugs turns vincent adapter names into `--agent` slugs, in table order and
// deduplicated. An empty or wholly unrecognized list means all three: a user
// who asks vincent to install a skill and has no adapter detected still means
// "put it where my agents look".
func Slugs(adapters []string) []string {
	want := map[string]bool{}
	for _, a := range adapters {
		want[strings.ToLower(strings.TrimSpace(a))] = true
	}
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		if want[a.Adapter] {
			out = append(out, a.Slug)
		}
	}
	if len(out) == 0 {
		for _, a := range agents {
			out = append(out, a.Slug)
		}
	}
	return out
}

// InstallArgs is the argv vincent hands `npx`, without the program name.
//
// It is the published command plus `--agent`, `--yes` and `--global`, and the
// difference is deliberate: `skills add` with no agent selection opens an
// interactive multi-select, which cannot be driven from a TUI takeover or a
// non-TTY CLI. `-g` is spelled `--global` for the same reason the rest is
// spelled out — this argv is asserted by a test, and a test that pins short
// flags pins nothing a reader can check against the documentation.
func InstallArgs(name string, slugs []string) []string {
	if len(slugs) == 0 {
		slugs = Slugs(nil)
	}
	return []string{
		"skills", "add", Repo,
		"--skill", name,
		"--agent", strings.Join(slugs, ","),
		"--yes",
		"--global",
	}
}

// Command is InstallArgs as a line a human can paste. It is what the
// missing-`npx` outcome prints, and what the TUI shows before it runs
// anything.
func Command(name string, slugs []string) string {
	return installer + " " + strings.Join(InstallArgs(name, slugs), " ")
}

// Install runs it, streaming the CLI's own output to out. One invocation per
// skill: `--skill` takes one name in the published command, and inventing a
// repeated or comma-joined spelling for it would be guessing at another
// tool's parser.
//
// The first run downloads the package, so this is slow and needs network.
// Callers must not block an event loop on it.
func Install(ctx context.Context, name string, slugs []string, out io.Writer) error {
	if _, err := exec.LookPath(installer); err != nil {
		return fmt.Errorf("%w — install node, then run: %s", ErrNoInstaller, Command(name, slugs))
	}
	//nolint:gosec // G204: the program is the constant `npx` resolved on PATH, and
	// every argument is built by InstallArgs from this package's own constants.
	cmd := exec.CommandContext(ctx, installer, InstallArgs(name, slugs)...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", Command(name, slugs), err)
	}
	return nil
}

// Missing is the subset of a report that is not installed and current — what
// the TUI offers to fix and what `vincent skills install` defaults to.
func Missing(statuses []Status) []string {
	names := make([]string, 0, len(statuses))
	for _, s := range statuses {
		if s.State != StateCurrent {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	return names
}
