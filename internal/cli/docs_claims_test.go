package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"go/types"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/tools/go/packages"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/taskstate"
)

// The published pages make claims about this command tree — which
// subcommands exist, and which columns their tables carry. Nothing enforced
// them, so both drifted: task 001's cleanup guidance rests on a branch column
// `vincent task ls` has never rendered (issue #137). These tests hold every
// user-facing page to what the tree and its tables actually do, in the
// package that owns both.
//
// They are drift checks, not feature checks: a page that stops making the
// claim satisfies them exactly as well as a command that starts backing it.

// docPages are the user-facing pages. `docs/tasks/` and `docs/history/` are
// maintainer records of what was planned and decided — they are allowed to
// describe a surface that does not exist yet — and `docs/spec.md` describes
// the API rather than the CLI.
func docPages(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..")
	pages := map[string]string{"README.md": ""}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		slashed := filepath.ToSlash(rel)
		if d.IsDir() {
			switch slashed {
			case "docs/tasks", "docs/history", "docs/gates", "docs/assets":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(slashed, ".md") && slashed != "docs/spec.md" {
			pages[slashed] = ""
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	for name := range pages {
		b, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		pages[name] = string(b)
	}
	return pages
}

// TestDocsClaimsTaskListShowsBranch: four pages send a reader to `vincent task
// ls --archived` to find the branches vincent made — the cleanup path task 001
// adopted when configurable names broke `git branch --list 'vincent/*'`. The
// table has no such column, and `--json` has no such field, so the claim has
// never been true.
func TestDocsClaimsTaskListShowsBranch(t *testing.T) {
	var claims []string
	for name, body := range docPages(t) {
		for i, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "task ls") && strings.Contains(strings.ToLower(line), "branch") {
				claims = append(claims, name+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	if len(claims) == 0 {
		t.Skip("no page claims `task ls` reports a branch")
	}

	out := runTaskLs(t, "--archived")
	header, _, _ := strings.Cut(out, "\n")
	columns := strings.Fields(header)
	if !slices.Contains(columns, "BRANCH") {
		t.Errorf("`vincent task ls --archived` columns = %v, want a BRANCH column claimed by:\n  %s",
			columns, strings.Join(claims, "\n  "))
	}

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(runTaskLs(t, "--archived", "--json")), &rows); err != nil {
		t.Fatalf("`task ls --json` is not JSON: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("`task ls --json` returned no rows")
	}
	if _, ok := rows[0]["branch_name"]; !ok {
		t.Errorf("`task ls --json` row has no branch_name (keys drop it in apiclient.Task), "+
			"so no output mode backs:\n  %s", strings.Join(claims, "\n  "))
	}
}

// parityClaims names the pages stating that everything the TUI does is also a
// subcommand, in a stable order. The match runs over whitespace-collapsed
// bodies, so a claim wrapped across lines still counts.
func parityClaims(t *testing.T) []string {
	t.Helper()
	parity := regexp.MustCompile(`(?i)everything the tui (?:does|can do) is (?:also )?a subcommand`)
	collapse := regexp.MustCompile(`\s+`)
	var claims []string
	for name, body := range docPages(t) {
		if parity.MatchString(collapse.ReplaceAllString(body, " ")) {
			claims = append(claims, name)
		}
	}
	slices.Sort(claims)
	return claims
}

// TestDocsClaimsTUIActionsHaveSubcommands: README, quickstart and the
// scripting guide state that everything the TUI does is also a subcommand.
// The TUI offers every §6 human action; `vincent task` exposed cancel alone of
// the ten until task 048 filled the gap (#89), and this keeps the §6 actions
// from drifting apart again. TestDocsClaimsTUIClientCallsHaveSubcommands
// covers the rest of what the TUI does.
func TestDocsClaimsTUIActionsHaveSubcommands(t *testing.T) {
	claims := parityClaims(t)
	if len(claims) == 0 {
		t.Skip("no page claims TUI/CLI parity")
	}

	have := map[string]bool{}
	for _, sub := range taskCmdNames() {
		have[sub] = true
	}
	seen := map[taskstate.Action]bool{}
	var missing []string
	for _, state := range taskstate.All {
		for _, action := range taskstate.HumanActionsFrom(state) {
			if seen[action] {
				continue
			}
			seen[action] = true
			// The API spells an action in snake_case and the command tree in
			// kebab-case — `follow_up` is `vincent task follow-up`. The same
			// action either way, so the comparison is on the CLI's spelling.
			if !have[strings.ReplaceAll(string(action), "_", "-")] {
				missing = append(missing, string(action))
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("`vincent task` has no subcommand for the human actions %v, "+
			"but these pages claim TUI/CLI parity: %v", missing, claims)
	}
}

// taskCmdNames lists the `vincent task` subcommands by their invoked name.
func taskCmdNames() []string {
	var names []string
	for _, c := range newTaskCmd().Commands() {
		names = append(names, c.Name())
	}
	return names
}

// tuiOnlyClientCalls are the apiclient.Client methods the TUI calls and no
// subcommand does, each with the decision that keeps it that way. Every entry
// is a row of "What only the TUI does" in docs/reference/cli.md, so the pages
// that claim parity can point at one list; an entry and its row are dropped
// together.
var tuiOnlyClientCalls = map[string]string{
	// The workflow editor.
	"CreateWorkflow": "the workflow editor; task 065 gave it no CLI counterpart — `workflow init`, `$EDITOR` and `workflow validate` are the shell's route",
	"PatchWorkflow":  "the workflow editor; task 065 gave it no CLI counterpart — `workflow init`, `$EDITOR` and `workflow validate` are the shell's route",
	"WorkflowSchema": "the workflow editor; task 065 gave it no CLI counterpart — `workflow init`, `$EDITOR` and `workflow validate` are the shell's route",
	// A task's Workflow tab.
	"GetTaskWorkflow": "the task Workflow tab's graph; 017 decision 20 and 051 declined `vincent task workflow`",
	// The triggers view.
	"Triggers":          "the triggers view's reads and dry poll; 096 decision 31H (narrowed by 098 decision 3) keeps the CLI to `trigger test|validate|ls|apply`",
	"Trigger":           "the triggers view's reads and dry poll; 096 decision 31H (narrowed by 098 decision 3) keeps the CLI to `trigger test|validate|ls|apply`",
	"TriggerSchema":     "the triggers view's reads and dry poll; 096 decision 31H (narrowed by 098 decision 3) keeps the CLI to `trigger test|validate|ls|apply`",
	"TriggerDeliveries": "the triggers view's reads and dry poll; 096 decision 31H (narrowed by 098 decision 3) keeps the CLI to `trigger test|validate|ls|apply`",
	"PollTrigger":       "the triggers view's reads and dry poll; 096 decision 31H (narrowed by 098 decision 3) keeps the CLI to `trigger test|validate|ls|apply`",
	"CreateTrigger":     "trigger authoring and arming; 098 decision 3 — humans arm in the TUI or `$EDITOR`, and `trigger apply` never arms",
	"PatchTrigger":      "trigger authoring and arming; 098 decision 3 — humans arm in the TUI or `$EDITOR`, and `trigger apply` never arms",
	"DeleteTrigger":     "trigger authoring and arming; 098 decision 3 — humans arm in the TUI or `$EDITOR`, and `trigger apply` never arms",
	// Previews and summaries.
	"Resolve": "the new-task and workflows preview; `vincent workflow render` prints the same §8.6 triple from the same resolver",
	"Info":    "the daemon summary; its fields are `daemon status`, `agents`, `doctor` and `config get`, and live slot usage stays TUI-only",
	// Transport, not capability: the SSE routes stay open to curl.
	"StreamEvents": "live transport; subcommands poll (`task transcript -f`, `chat transcript -f`, `chat send`)",
	"StreamTask":   "live transport; subcommands poll (`task transcript -f`, `chat transcript -f`, `chat send`)",
	"StreamChat":   "live transport; subcommands poll (`task transcript -f`, `chat transcript -f`, `chat send`)",
}

// TestDocsClaimsTUIClientCallsHaveSubcommands: the parity claim covers more
// than §6's actions. Everything the TUI does reaches the daemon through an
// apiclient.Client method, so a method the TUI calls and internal/cli never
// does is something only the TUI does — and it must be on tuiOnlyClientCalls,
// which the CLI reference publishes, or the claim is false (issue #395).
//
// The call map comes from go/types, not a name scan: every Bubble Tea model
// has an Update method and so does apiclient.Client, so matching `.Update(`
// syntactically would report gaps that are not there. An exemption that goes
// stale — the TUI stopped calling it, or a subcommand started — fails too, so
// the published list cannot keep an exception that no longer is one.
func TestDocsClaimsTUIClientCallsHaveSubcommands(t *testing.T) {
	claims := parityClaims(t)
	if len(claims) == 0 {
		t.Skip("no page claims TUI/CLI parity")
	}

	tui, cli := clientCallSites(t)
	if len(tui) == 0 {
		t.Fatal("found no apiclient.Client calls in internal/tui; the load saw nothing")
	}

	var gaps []string
	for _, method := range slices.Sorted(maps.Keys(tui)) {
		if _, ok := cli[method]; ok {
			continue
		}
		if _, ok := tuiOnlyClientCalls[method]; ok {
			continue
		}
		gaps = append(gaps, method+" ("+tui[method]+")")
	}
	if len(gaps) > 0 {
		t.Errorf("the TUI calls apiclient.Client methods no subcommand calls:\n  %s\n"+
			"but these pages claim TUI/CLI parity: %v\n"+
			"add a subcommand, or an entry in tuiOnlyClientCalls plus its row under "+
			"\"What only the TUI does\" in docs/reference/cli.md",
			strings.Join(gaps, "\n  "), claims)
	}

	for _, method := range slices.Sorted(maps.Keys(tuiOnlyClientCalls)) {
		reason := tuiOnlyClientCalls[method]
		if _, ok := tui[method]; !ok {
			t.Errorf("tuiOnlyClientCalls exempts %s (%s), but the TUI no longer calls it; "+
				"drop the entry and its row in docs/reference/cli.md", method, reason)
		}
		if site, ok := cli[method]; ok {
			t.Errorf("tuiOnlyClientCalls exempts %s (%s), but a subcommand now calls it (%s); "+
				"drop the entry and its row in docs/reference/cli.md", method, reason, site)
		}
	}
}

// clientCallSites maps every apiclient.Client method called from non-test code
// in internal/tui (and its subpackages) and in internal/cli to its first call
// site, as a slash-separated repo-relative `file:line`.
func clientCallSites(t *testing.T) (tui, cli map[string]string) {
	t.Helper()
	const (
		module    = "github.com/lezli01/vincent"
		apiclient = module + "/internal/apiclient"
		tuiPkg    = module + "/internal/tui"
		cliPkg    = module + "/internal/cli"
	)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	pkgs, err := packages.Load(&packages.Config{
		Mode:  packages.NeedName | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:   root,
		Tests: false,
	}, "./internal/tui/...", "./internal/cli")
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}

	type site struct {
		file string
		line int
	}
	first := func(sites map[string]site, method string, s site) {
		if old, ok := sites[method]; !ok || s.file < old.file || (s.file == old.file && s.line < old.line) {
			sites[method] = s
		}
	}
	tuiSites, cliSites := map[string]site{}, map[string]site{}
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			t.Errorf("load %s: %v", pkg.PkgPath, e)
		}
		var sites map[string]site
		switch {
		case pkg.PkgPath == tuiPkg || strings.HasPrefix(pkg.PkgPath, tuiPkg+"/"):
			sites = tuiSites
		case pkg.PkgPath == cliPkg:
			sites = cliSites
		default:
			t.Fatalf("load returned unexpected package %s", pkg.PkgPath)
		}
		if pkg.TypesInfo == nil {
			t.Fatalf("load %s: no type information", pkg.PkgPath)
		}
		for expr, sel := range pkg.TypesInfo.Selections {
			fn, ok := sel.Obj().(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != apiclient {
				continue
			}
			recv := fn.Signature().Recv()
			if recv == nil {
				continue
			}
			typ := types.Unalias(recv.Type())
			if ptr, isPtr := typ.(*types.Pointer); isPtr {
				typ = types.Unalias(ptr.Elem())
			}
			named, ok := typ.(*types.Named)
			if !ok || named.Obj().Name() != "Client" {
				continue
			}
			pos := pkg.Fset.Position(expr.Sel.Pos())
			// The path only labels a failure, so one go list reports under a
			// different spelling of the root (a symlink, a drive letter's
			// case) stays absolute rather than failing the test.
			file := pos.Filename
			if rel, relErr := filepath.Rel(root, file); relErr == nil && filepath.IsLocal(rel) {
				file = rel
			}
			first(sites, fn.Name(), site{file: filepath.ToSlash(file), line: pos.Line})
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	flatten := func(sites map[string]site) map[string]string {
		out := make(map[string]string, len(sites))
		for method, s := range sites {
			out[method] = s.file + ":" + strconv.Itoa(s.line)
		}
		return out
	}
	return flatten(tuiSites), flatten(cliSites)
}

// stubTask is one row of GET /v1/tasks as the daemon serves it — the server's
// listTaskResponse embeds taskResponse, so branch_name is on the wire and only
// the client drops it.
const stubTask = `[{
  "id": 7,
  "project_id": 1,
  "project_name": "vincent",
  "title": "File the release issue",
  "workflow": "github-create-issue",
  "state": "archived",
  "base_branch": "main",
  "branch_name": "vincent/7-file-the-release-issue",
  "worktree_path": null,
  "priority": 0,
  "current_step": 1,
  "step_total": 1,
  "step_name": "file",
  "block_reason": null,
  "pause_requested": false,
  "available_actions": [],
  "queued_reason": null,
  "admit_not_before": null,
  "cost_usd": null,
  "input_tokens": 0,
  "output_tokens": 0,
  "created_at": "2026-08-19T10:00:00Z",
  "updated_at": "2026-08-19T10:05:00Z",
  "started_at": null,
  "finished_at": null,
  "archived_at": "2026-08-19T10:05:00Z"
}]`

// runTaskLs runs `vincent task ls` in-process against a stub daemon: the
// rendering is the assertion, so nothing here needs a real store, worktree or
// agent.
func runTaskLs(t *testing.T, args ...string) string {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv(config.EnvDataDir, dataDir)
	t.Setenv(config.EnvConfigDir, t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/tasks":
			_, _ = w.Write([]byte(stubTask))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse stub URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("stub port: %v", err)
	}
	if _, err := daemon.EnsureToken(dataDir); err != nil {
		t.Fatalf("token: %v", err)
	}
	if err := daemon.WriteRuntimeInfo(dataDir, daemon.RuntimeInfo{
		Port: port, PID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("daemon.json: %v", err)
	}

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append([]string{"task", "ls"}, args...))
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("task ls: %v (%s)", err, buf.String())
	}
	return buf.String()
}

// TestDocsClaimsEveryCommandIsOnTheCLIPage: `docs/reference/cli.md` tracks the
// cobra tree (CLAUDE.md's documentation model). A command the tree grows and
// the page never mentions is the drift this file exists to catch — task 047
// added two, and this is what keeps the next two from arriving unannounced.
//
// It asks only that the page names the command, not how: `vincent service
// install` earns its mention inside a shell block rather than a heading, and
// that is a legitimate way to document three sibling commands at once.
func TestDocsClaimsEveryCommandIsOnTheCLIPage(t *testing.T) {
	page, ok := docPages(t)["docs/reference/cli.md"]
	if !ok {
		t.Fatal("docs/reference/cli.md is missing")
	}
	var missing []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.CommandPath() != "vincent" && !strings.Contains(page, c.CommandPath()) {
			missing = append(missing, c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd())
	if len(missing) > 0 {
		t.Errorf("the CLI reference never mentions %v", missing)
	}
}

// TestDocsClaimsEveryFlagIsOnTheCLIPage: the same drift check one level down.
// A flag the tree grows and the page never spells out is invisible to anyone
// reading the reference — `--lines` and `--follow` shipped as `-n` and `-f`
// only, and a reader had no way to learn the long forms existed.
//
// Like its neighbour it asks only that the page names the long form somewhere,
// not where or how: this is a drift check, not a feature check. Hidden flags
// and cobra's automatic `help` are not documentation's to carry.
func TestDocsClaimsEveryFlagIsOnTheCLIPage(t *testing.T) {
	page, ok := docPages(t)["docs/reference/cli.md"]
	if !ok {
		t.Fatal("docs/reference/cli.md is missing")
	}
	var missing []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			if f.Hidden || f.Name == "help" {
				return
			}
			if !strings.Contains(page, "--"+f.Name) {
				missing = append(missing, c.CommandPath()+" --"+f.Name)
			}
		}
		c.LocalNonPersistentFlags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd())
	if len(missing) > 0 {
		t.Errorf("the CLI reference never mentions %v", missing)
	}
}
