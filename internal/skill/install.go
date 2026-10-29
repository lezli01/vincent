package skill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	// Store marks an agent that reads the global store, `~/.agents/skills`,
	// itself (task 138.12). It has no slug — there is nothing for `skills
	// add` to link — and detection reports the store copy as read by it
	// whenever its root exists on this machine.
	Store bool
}

// agents is that table, in the order the argv lists them. Only vincent's
// adapters are here — this decides what vincent *installs for*, not what it
// *reports*: detection scans the home directory, so an agent vincent does not
// drive still appears in a row when it holds a link.
//
// copilot is the store-resident row (task 138 decision 11): copilot 1.0.x
// loads `~/.agents/skills` as its `personal-agents` source, so vincent links
// nothing for it. Its Root still matters: a real `~/.copilot/skills/<name>`,
// left by running the skills CLI with `github-copilot`, is copilot's link.
var agents = []agentSlug{
	{Adapter: "claude", Slug: "claude-code", Root: ".claude"},
	{Adapter: "codex", Slug: "codex", Root: ".codex"},
	{Adapter: "copilot", Root: ".copilot", Store: true},
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

// storeResidentRoot reports whether a home directory belongs to an agent that
// reads the store itself.
func storeResidentRoot(root string) bool {
	for _, a := range agents {
		if a.Root == root {
			return a.Store
		}
	}
	return false
}

// StoreResident reports whether a vincent adapter reads the global store
// itself, so installing for it links nothing.
func StoreResident(adapter string) bool {
	adapter = strings.ToLower(strings.TrimSpace(adapter))
	for _, a := range agents {
		if a.Adapter == adapter {
			return a.Store
		}
	}
	return false
}

// Slugs turns vincent adapter names into `--agent` slugs, in table order and
// deduplicated. An empty or wholly unrecognized list means all three: a user
// who asks vincent to install a skill and has no adapter detected still means
// "put it where my agents look". A store-resident adapter contributes no
// slug, and a list naming only those gives none — it was recognized, and what
// it asks for is already served by the store (see PlanInstall).
func Slugs(adapters []string) []string {
	want := map[string]bool{}
	for _, a := range adapters {
		want[strings.ToLower(strings.TrimSpace(a))] = true
	}
	out := make([]string, 0, len(agents))
	recognized := false
	for _, a := range agents {
		if !want[a.Adapter] {
			continue
		}
		recognized = true
		if a.Slug != "" {
			out = append(out, a.Slug)
		}
	}
	if !recognized {
		for _, a := range agents {
			if a.Slug != "" {
				out = append(out, a.Slug)
			}
		}
	}
	return out
}

// Plan is what installing one skill for an adapter selection means. Slugs
// non-empty is a `skills add` run. Slugs empty is a selection of
// store-resident agents only, where nothing is linked: Note says the store
// already serves it, or Err says there is no store copy to serve.
type Plan struct {
	Slugs []string
	Note  string
	Err   error
}

// Run reports whether the plan shells out at all.
func (p Plan) Run() bool { return len(p.Slugs) > 0 }

// PlanInstall decides one skill's install for adapters, against the report
// statuses — the one rule the CLI and the TUI both apply, so the two cannot
// drift (task 138.12 decision 4). Any adapter with a slug makes it a plain
// `skills add` for those slugs; a store-resident adapter adds nothing to the
// argv. Only when every recognized adapter is store-resident does the store
// copy decide: present is success with nothing to run, absent is an error
// naming how to populate it. A store copy that is there but not current —
// older, differs, unreadable — is not served either: copilot would read the
// stale copy, and calling that installed would make `skills install` exit 0
// on exactly the skills Missing offered to fix (review F2). It is an error
// naming the same remedy, since installing for another agent refreshes the
// store.
func PlanInstall(name string, adapters []string, statuses []Status) Plan {
	slugs := Slugs(adapters)
	if len(slugs) > 0 {
		return Plan{Slugs: slugs}
	}
	readers := make([]string, 0, 1)
	for _, a := range agents {
		for _, want := range adapters {
			if a.Store && strings.EqualFold(strings.TrimSpace(want), a.Adapter) {
				readers = append(readers, a.Adapter)
				break
			}
		}
	}
	who := strings.Join(readers, ", ")
	storePath, state := "", ""
	for _, s := range statuses {
		if s.Name == name {
			storePath, state = s.StorePath, s.State
			break
		}
	}
	if storePath != "" {
		if _, err := os.Lstat(storePath); err == nil {
			if state != StateCurrent {
				return Plan{Err: fmt.Errorf("%s reads the store copy at %s, which is %s — "+
					"update it for another agent, e.g. vincent skills install %s --agent claude, "+
					"which refreshes the store", who, storePath, state, name)}
			}
			return Plan{Note: who + " reads " + storePath + " directly; there is nothing to link"}
		}
	} else {
		storePath = filepath.Join("~", StoreDir, skillsSubdir, name)
	}
	return Plan{Err: fmt.Errorf("%s reads the store copy at %s and none is installed — "+
		"install it for another agent first, e.g. vincent skills install %s --agent claude, "+
		"which populates the store", who, storePath, name)}
}

// InstallArgs is the argv vincent hands `npx`, without the program name.
//
// It is the published command plus `--agent`, `--yes` and `--global`, and the
// difference is deliberate: `skills add` with no agent selection opens an
// interactive multi-select, which cannot be driven from a TUI takeover or a
// non-TTY CLI. `-g` is spelled `--global` for the same reason the rest is
// spelled out — this argv is asserted by a test, and a test that pins short
// flags pins nothing a reader can check against the documentation.
//
// `--agent` is variadic on the skills CLI: it consumes every following
// argument until the next flag. Joining slugs with commas made one fake
// agent name (`claude-code,codex,cursor`) that the CLI rejects. Each slug
// is its own argv element so the published parser sees three agents.
func InstallArgs(name string, slugs []string) []string {
	if len(slugs) == 0 {
		slugs = Slugs(nil)
	}
	args := []string{
		"skills", "add", Repo,
		"--skill", name,
		"--agent",
	}
	args = append(args, slugs...)
	return append(args, "--yes", "--global")
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
