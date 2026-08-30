package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("got %+v, want defaults %+v", cfg, Default())
	}
}

func TestLoadPartialOverridesKeepDefaults(t *testing.T) {
	path := writeConfig(t, "max_parallel_tasks: 5\nlog_level: debug\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxParallelTasks != 5 {
		t.Errorf("MaxParallelTasks = %d, want 5", cfg.MaxParallelTasks)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.Listen != Default().Listen {
		t.Errorf("Listen = %q, want default %q", cfg.Listen, Default().Listen)
	}
	if cfg.Defaults.AgentTimeout != Default().Defaults.AgentTimeout {
		t.Errorf("AgentTimeout = %s, want default %s", cfg.Defaults.AgentTimeout, Default().Defaults.AgentTimeout)
	}
}

func TestLoadFullOverrides(t *testing.T) {
	path := writeConfig(t, strings.TrimSpace(`
listen: localhost:7777
max_parallel_tasks: 9
defaults:
  agent_timeout: 1h30m
  command_timeout: 90s
  input_timeout: 36h
transcript_retention_days: 7
transcript_max_bytes: 32MB
max_task_cost_usd: 12.5
usage_limit_recheck_interval: 2m
log_level: warn
agents:
  claude:
    path: /opt/bin/claude
  codex:
    path: C:\tools\codex.exe
`))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Listen:           "localhost:7777",
		MaxParallelTasks: 9,
		MaxParallelChats: 3,
		Defaults: Defaults{
			AgentTimeout:   Duration(90 * time.Minute),
			CommandTimeout: Duration(90 * time.Second),
			InputTimeout:   Duration(36 * time.Hour),
		},
		// Same property as `environment:` below: the file names neither branch
		// key nor `fetch_base_branch`, so the §10 defaults survive an otherwise
		// fully-overriding config — on locally, off for the forge (task 008),
		// and fetching the base before a worktree is cut (task 056).
		DeleteEmptyBranchOnArchive: true,
		FetchBaseBranch:            true,
		TranscriptRetentionDays:    7,
		TranscriptMaxBytes:         32 << 20,
		MaxTaskCostUSD:             12.5,
		UsageLimitRecheckInterval:  Duration(2 * time.Minute),
		LogLevel:                   "warn",
		// The file omits `environment:`, so the §12.3 default survives an
		// otherwise fully-overriding config — which is the property that
		// keeps T4.23 invisible to anyone who does not ask for it.
		Environment: Environment{Inherit: InheritAll()},
		Agents: Agents{
			Claude: Agent{Path: "/opt/bin/claude"},
			Codex:  Agent{Path: `C:\tools\codex.exe`},
		},
		// And the same again for `parallel:` and `tui:`: an unnamed section
		// keeps its default rather than collapsing to a zero value — a
		// max_parallel of 0 would refuse to run any sub-step at all.
		Parallel: Parallel{MaxParallel: 4},
		FanOut:   FanOut{MaxDepth: 3, MaxTasks: 64},
		Loop:     Loop{MaxIterations: 10},
		Include:  Include{MaxDepth: 5},
		// And once more for `github:` — the file names no key, so the task
		// 035 opt-out default survives: enabled, with task 052's reconciler
		// interval.
		GitHub: GitHub{Enabled: true, PollInterval: Duration(5 * time.Minute)},
		// And for `update:` — the release check is on by default (task 055)
		// and stays on through a config that overrides everything else, which
		// is what "opt-out" has to mean for it to be true.
		Update: Update{Check: true, PollInterval: Duration(24 * time.Hour)},
		MCP:    MCP{WireSteps: true, MaxDepth: 3, MaxTasks: 32},
		// And for `container:` — the file names no key, so the §16 default
		// survives: no image, which is the whole switch. An installation that
		// overrides everything else still runs every step on the host.
		Container: Container{Runtime: "docker", MountAgentConfig: true, Network: true},
		TUI:       TUI{Board: BoardView{GroupBy: []BoardGroup{BoardGroupProject, BoardGroupWorkflow}}},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

// TestBranchCleanupDefaults pins the split default the §10 amendment rests on
// (task 008): the local leg is on, the leg that writes to a forge is not.
func TestBranchCleanupDefaults(t *testing.T) {
	d := Default()
	if !d.DeleteEmptyBranchOnArchive {
		t.Error("delete_empty_branch_on_archive defaults to false, want true")
	}
	if d.DeleteRemoteBranchOnArchive {
		t.Error("delete_remote_branch_on_archive defaults to true, want false")
	}
}

// TestParallelMaxParallel pins the task 014 default and the floor under it: a
// group bounded at zero would deadlock rather than run serially, so the value
// is rejected at load time instead of being quietly clamped.
func TestParallelMaxParallel(t *testing.T) {
	if got := Default().Parallel.MaxParallel; got != 4 {
		t.Errorf("parallel.max_parallel default = %d, want 4", got)
	}
	cfg, err := Load(writeConfig(t, "parallel:\n  max_parallel: 8\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Parallel.MaxParallel != 8 {
		t.Errorf("parallel.max_parallel = %d, want the configured 8", cfg.Parallel.MaxParallel)
	}
	if _, err := Load(writeConfig(t, "parallel:\n  max_parallel: 0\n")); err == nil {
		t.Error("max_parallel: 0 loaded without error, want a validation failure")
	}
}

// TestBranchCleanupExplicitFalse: a plain bool is only correct if an explicit
// `false` in the file survives unmarshalling into Default() — that is the one
// way back to the pre-008 behaviour.
func TestBranchCleanupExplicitFalse(t *testing.T) {
	cfg, err := Load(writeConfig(t, "delete_empty_branch_on_archive: false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DeleteEmptyBranchOnArchive {
		t.Error("an explicit false was overwritten by the default true")
	}
}

// TestBranchCleanupWarnsOnUnreachableRemoteLeg: the pair still loads — a key
// that is merely unreachable is not an invalid one, and failing the file over
// it would revert every unrelated edit in the same save — but it must not be
// silent.
func TestBranchCleanupWarnsOnUnreachableRemoteLeg(t *testing.T) {
	cfg, err := Load(writeConfig(t, strings.TrimSpace(`
delete_empty_branch_on_archive: false
delete_remote_branch_on_archive: true
`)))
	if err != nil {
		t.Fatalf("Load: %v — an unreachable key must not refuse the file", err)
	}
	warnings := cfg.Warnings()
	if len(warnings) != 1 {
		t.Fatalf("Warnings() = %q, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], "delete_remote_branch_on_archive") {
		t.Errorf("warning %q does not name the key it is about", warnings[0])
	}
	// Every workable combination stays quiet, or the log stops meaning anything.
	for _, c := range []Config{
		Default(),
		{DeleteEmptyBranchOnArchive: true, DeleteRemoteBranchOnArchive: true},
		{},
	} {
		if w := c.Warnings(); len(w) != 0 {
			t.Errorf("Warnings() = %q for %+v, want none", w, c)
		}
	}
}

// TestTaskCostCapDefaultsOff pins task 033's "nothing changes for anyone who
// does not ask": the built-in default is zero, an explicit zero is the same
// value, and zero is the documented way to say "no cap" — so neither an
// untouched config nor a deliberate `max_task_cost_usd: 0` can be refused.
func TestTaskCostCapDefaultsOff(t *testing.T) {
	if got := Default().MaxTaskCostUSD; got != 0 {
		t.Errorf("max_task_cost_usd default = %v, want 0 (off)", got)
	}
	for content, want := range map[string]float64{
		"max_task_cost_usd: 0\n":   0,
		"max_task_cost_usd: 2.5\n": 2.5,
		"max_task_cost_usd: 5\n":   5, // an integer is a perfectly good budget
	} {
		cfg, err := Load(writeConfig(t, content))
		if err != nil {
			t.Fatalf("Load(%q): %v", content, err)
		}
		if cfg.MaxTaskCostUSD != want {
			t.Errorf("Load(%q) = %v, want %v", content, cfg.MaxTaskCostUSD, want)
		}
	}
}

// TestBoardGroupingDefault pins §15's default: a board is read project by
// project, and within a project by what each task is doing.
func TestBoardGroupingDefault(t *testing.T) {
	want := []BoardGroup{BoardGroupProject, BoardGroupWorkflow}
	if got := Default().TUI.Board.GroupBy; !reflect.DeepEqual(got, want) {
		t.Errorf("default group_by = %v, want %v", got, want)
	}
}

// TestBoardGroupingExplicitFlat: an empty list has to survive unmarshalling
// into Default(), or there is no way back to the flat table.
func TestBoardGroupingExplicitFlat(t *testing.T) {
	cfg, err := Load(writeConfig(t, "tui:\n  board:\n    group_by: []\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TUI.Board.GroupBy) != 0 {
		t.Errorf("group_by = %v, want the empty list the file asked for", cfg.TUI.Board.GroupBy)
	}
}

// TestBoardGroupingOneLevel: a single level is a whole configuration, not a
// half-written default.
func TestBoardGroupingOneLevel(t *testing.T) {
	cfg, err := Load(writeConfig(t, "tui:\n  board:\n    group_by: [workflow]\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []BoardGroup{BoardGroupWorkflow}; !reflect.DeepEqual(cfg.TUI.Board.GroupBy, want) {
		t.Errorf("group_by = %v, want %v", cfg.TUI.Board.GroupBy, want)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"unknown key":           "max_parallel_jobs: 5\n",
		"unknown nested key":    "defaults:\n  agent_timeut: 60m\n",
		"bad duration":          "defaults:\n  agent_timeout: sixty minutes\n",
		"duration missing unit": "defaults:\n  agent_timeout: 60\n",
		"negative duration":     "defaults:\n  command_timeout: -5m\n",
		"zero cap":              "max_parallel_tasks: 0\n",
		"bad log level":         "log_level: verbose\n",
		"non-loopback listen":   "listen: 0.0.0.0:8080\n",
		"listen without port":   "listen: 127.0.0.1\n",
		"bad port":              "listen: 127.0.0.1:notaport\n",
		"negative retention":    "transcript_retention_days: -1\n",
		// Non-negative, not positive: zero is the documented "no cap"
		// (task 033), so only a budget no run could honour is refused.
		"negative task cost cap":        "max_task_cost_usd: -1\n",
		"task cost cap is not a number": "max_task_cost_usd: five dollars\n",
		// Zero would re-admit a quota-held task on the very next tick, which
		// is the respawn loop the hold exists to stop (task 003).
		"zero recheck interval":     "usage_limit_recheck_interval: 0s\n",
		"negative recheck interval": "usage_limit_recheck_interval: -1m\n",
		"not yaml":                  "{{{\n",
		"wrong type for integer":    "max_parallel_tasks: many\n",
		"unknown grouping level":    "tui:\n  board:\n    group_by: [project, agent]\n",
		"repeated grouping level":   "tui:\n  board:\n    group_by: [project, project]\n",
		"grouping is not a list":    "tui:\n  board:\n    group_by: project\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, content)); err == nil {
				t.Errorf("Load accepted invalid config %q", content)
			}
		})
	}
}

func TestEnsureDefaultFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg") // exercise dir creation too
	created, err := EnsureDefaultFile(dir)
	if err != nil {
		t.Fatalf("EnsureDefaultFile: %v", err)
	}
	if !created {
		t.Error("created = false on first call, want true")
	}

	// The generated file must parse strictly and equal the built-in defaults.
	cfg, err := Load(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("Load(generated default): %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("generated file loads to %+v, want defaults %+v", cfg, Default())
	}

	// A second call must not touch the existing file.
	custom := "max_parallel_tasks: 42\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err = EnsureDefaultFile(dir)
	if err != nil {
		t.Fatalf("EnsureDefaultFile (second): %v", err)
	}
	if created {
		t.Error("created = true on second call, want false")
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != custom {
		t.Error("EnsureDefaultFile overwrote an existing config file")
	}
}

// The release check is an opt-out with two independent switches, and each
// must work on its own: `check: false` keeps a poll interval a user may want
// back, and `poll_interval: 0` keeps the check legible while stopping it
// (task 055).
func TestUpdateCheckSwitches(t *testing.T) {
	for _, tc := range []struct {
		name  string
		yaml  string
		polls bool
	}{
		{"default", "listen: 127.0.0.1:7777\n", true},
		{"check false", "listen: 127.0.0.1:7777\nupdate:\n  check: false\n", false},
		{"interval zero", "listen: 127.0.0.1:7777\nupdate:\n  poll_interval: 0\n", false},
		{"both", "listen: 127.0.0.1:7777\nupdate:\n  check: false\n  poll_interval: 0\n", false},
		{"explicit interval", "listen: 127.0.0.1:7777\nupdate:\n  poll_interval: 1h\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.yaml)
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Update.Polls(); got != tc.polls {
				t.Errorf("Update.Polls() = %v, want %v (%+v)", got, tc.polls, cfg.Update)
			}
		})
	}
}

// A negative interval is a typo that would otherwise round to "do not poll"
// and look like it worked, so it refuses the file.
func TestUpdateNegativePollIntervalRefused(t *testing.T) {
	path := writeConfig(t, "listen: 127.0.0.1:7777\nupdate:\n  poll_interval: -1h\n")
	if _, err := Load(path); err == nil {
		t.Fatal("a negative update.poll_interval loaded")
	}
}
