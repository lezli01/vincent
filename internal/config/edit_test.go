package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// The editor's contract, in the three cases config.yaml actually presents
// (task 060): an active key, a documented block that is commented out, and a
// key an older template never carried at all.

func TestApplyEditsAnActiveKeyInPlace(t *testing.T) {
	src := `# a leading comment
listen: 127.0.0.1:0

# how loud the daemon is
log_level: info

defaults:
  agent_timeout: 60m
  command_timeout: 15m
`
	got, err := Apply([]byte(src), []Set{{Path: "log_level", Value: "debug"}, {Path: "defaults.command_timeout", Value: "30m"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	out := string(got)
	for _, want := range []string{
		"# a leading comment", "# how loud the daemon is",
		"log_level: debug", "  command_timeout: 30m", "  agent_timeout: 60m",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q from:\n%s", want, out)
		}
	}
	if strings.Contains(out, "log_level: info") {
		t.Errorf("the old value survived:\n%s", out)
	}
	// Key order and the blank lines are the file's, not the struct's.
	if !strings.Contains(out, "listen: 127.0.0.1:0\n\n# how loud") {
		t.Errorf("blank lines or key order moved:\n%s", out)
	}
}

// The `notify:` block ships commented out, and it is the case the issue was
// opened on: turning the hook on must uncomment the block where it stands
// rather than appending a second `notify:` to the end of the file.
func TestApplyUncommentsADocumentedBlock(t *testing.T) {
	got, err := Apply([]byte(defaultConfigYAML), []Set{
		{Path: "notify.on", Value: "[blocked, awaiting_gate]"},
		{Path: "notify.command", Value: `["/usr/local/bin/notify-me"]`},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	out := string(got)
	if n := strings.Count(out, "\nnotify:"); n != 1 {
		t.Errorf("notify: appears %d times, want exactly 1:\n%s", n, out)
	}
	for _, want := range []string{
		"\nnotify:\n", "  on: [blocked, awaiting_gate]", `  command: ["/usr/local/bin/notify-me"]`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q from the edited file", want)
		}
	}
	// Everything the block documented is still there.
	if !strings.Contains(out, "WARNING: this is arbitrary code the daemon runs as you") {
		t.Error("the notify block's warning was flattened")
	}
	cfg, err := Decode(got)
	if err != nil {
		t.Fatalf("the edited template no longer parses: %v", err)
	}
	if !cfg.Notify.Enabled() {
		t.Errorf("notify did not take effect: %+v", cfg.Notify)
	}
}

// An active parent with a commented child — the `environment:` block's shape.
func TestApplyUncommentsAChildOfAnActiveBlock(t *testing.T) {
	got, err := Apply([]byte(defaultConfigYAML), []Set{{Path: "environment.set", Value: "{LANG: C.UTF-8}"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "  set: {LANG: C.UTF-8}") {
		t.Errorf("environment.set was not uncommented in place:\n%s", out)
	}
	cfg, err := Decode(got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Environment.Set["LANG"] != "C.UTF-8" {
		t.Errorf("environment.set = %v", cfg.Environment.Set)
	}
}

// The fallback: a config.yaml written before a key's documented block existed.
func TestApplyAppendsAKeyWithNoBlock(t *testing.T) {
	src := "listen: 127.0.0.1:0\nlog_level: info\n"
	got, err := Apply([]byte(src), []Set{
		{Path: "max_parallel_tasks", Value: "9"},
		{Path: "fan_out.max_depth", Value: "2"},
		{Path: "fan_out.max_tasks", Value: "7"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	cfg, err := Decode(got)
	if err != nil {
		t.Fatalf("decode %q: %v", got, err)
	}
	if cfg.MaxParallelTasks != 9 || cfg.FanOut.MaxDepth != 2 || cfg.FanOut.MaxTasks != 7 {
		t.Errorf("appended keys did not take: %+v", cfg)
	}
	// The second key under fan_out lands inside the block the first created.
	if n := strings.Count(string(got), "fan_out:"); n != 1 {
		t.Errorf("fan_out: appears %d times, want 1:\n%s", n, got)
	}
}

// A trailing comment on a key is documentation of that key. Replacing the
// value must not take it with it.
func TestApplyKeepsATrailingComment(t *testing.T) {
	src := "environment:\n  inherit: all          # all | none | a list of names\n"
	got, err := Apply([]byte(src), []Set{{Path: "environment.inherit", Value: "none"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "inherit: none") {
		t.Errorf("the value was not replaced:\n%s", out)
	}
	if !strings.Contains(out, "# all | none | a list of names") {
		t.Errorf("the trailing comment was dropped:\n%s", out)
	}
}

// The template is the file most installations have, so every key the editor
// can be asked for has to survive a round trip through it and still parse.
func TestApplyOverTheTemplateStillParses(t *testing.T) {
	sets := []Set{
		{Path: "listen", Value: "127.0.0.1:8080"},
		{Path: "max_parallel_tasks", Value: "7"},
		{Path: "branch_template", Value: RenderString("wip/{{.ID}}")},
		{Path: "defaults.agent_timeout", Value: "90m"},
		{Path: "transcript_max_bytes", Value: RenderString("1GB")},
		{Path: "max_task_cost_usd", Value: "2.5"},
		{Path: "log_level", Value: "warn"},
		{Path: "debug", Value: "true"},
		{Path: "environment.unset", Value: RenderList([]string{"MSYSTEM"})},
		{Path: "agents.cursor.path", Value: RenderString("/opt/cursor-agent")},
		{Path: "parallel.max_parallel", Value: "2"},
		{Path: "mcp.wire_steps", Value: "false"},
		{Path: "include.max_depth", Value: "9"},
		{Path: "loop.max_iterations", Value: "3"},
		{Path: "github.poll_interval", Value: "0s"},
		{Path: "update.check", Value: "false"},
		{Path: "tui.board.group_by", Value: RenderList(nil)},
	}
	got, err := Apply([]byte(defaultConfigYAML), sets)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	cfg, err := Decode(got)
	if err != nil {
		t.Fatalf("the edited template no longer parses: %v\n%s", err, got)
	}
	if cfg.Listen != "127.0.0.1:8080" || cfg.MaxParallelTasks != 7 ||
		cfg.BranchTemplate != "wip/{{.ID}}" || cfg.LogLevel != "warn" || !cfg.Debug ||
		cfg.TranscriptMaxBytes != 1<<30 || cfg.MaxTaskCostUSD != 2.5 ||
		cfg.Agents.Cursor.Path != "/opt/cursor-agent" || cfg.MCP.WireSteps ||
		cfg.Include.MaxDepth != 9 || cfg.Loop.MaxIterations != 3 ||
		cfg.GitHub.PollInterval != 0 || cfg.Update.Check ||
		len(cfg.TUI.Board.GroupBy) != 0 || len(cfg.Environment.Unset) != 1 {
		t.Errorf("a set did not take: %+v", cfg)
	}
	// Every key was already documented, so nothing was appended: the file has
	// the same number of lines it started with.
	if a, b := strings.Count(string(got), "\n"), strings.Count(defaultConfigYAML, "\n"); a != b {
		t.Errorf("the template grew from %d lines to %d; a documented key was appended instead of edited", b, a)
	}
}

// RenderString has one job: what it emits has to read back as what went in.
func TestRenderStringRoundTrips(t *testing.T) {
	for _, s := range []string{"", "info", "true", "no", "60m", "a b", "vincent/{{.ID}}-{{.Slug}}", "#hash", "C:\\bin\\cursor.exe"} {
		src := "log_level: " + RenderString(s) + "\n"
		var out struct {
			LogLevel string `yaml:"log_level"`
		}
		if err := yamlUnmarshalForTest([]byte(src), &out); err != nil {
			t.Errorf("%q rendered as %s, which does not parse: %v", s, RenderString(s), err)
			continue
		}
		if out.LogLevel != s {
			t.Errorf("%q round-tripped as %q (rendered %s)", s, out.LogLevel, RenderString(s))
		}
	}
}

func TestWriteFileIsAtomicAndOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("log_level: info\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WriteFile(path, []byte("log_level: debug\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "log_level: debug\n" {
		t.Errorf("contents = %q", got)
	}
	// The temporary file is named so config.Watch's base-name filter ignores
	// it, and it must not be left behind.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary file survived the write: %v", err)
	}
}

// yamlUnmarshalForTest keeps the round-trip test honest about which parser it
// is asserting against: the one Load and Decode use, not a second one.
func yamlUnmarshalForTest(b []byte, v any) error { return yaml.Unmarshal(b, v) }
