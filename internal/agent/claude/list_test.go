package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestSupportsSkillListingEdges pins task 124 decision 40's floor at its
// edges: the §7.4 input family, from 2.1.277 on. A build outside the family is
// a positive no whichever side it falls on.
func TestSupportsSkillListingEdges(t *testing.T) {
	for version, want := range map[string]bool{
		"2.1.276":               false,
		"2.1.277":               true,
		"2.1.277 (Claude Code)": true,
		"2.1.278":               true,
		"2.1.1000":              true,
		"2.2.0":                 true,
		"2.9.99":                true,
		"2.0.999":               false,
		"1.9.300":               false,
		"3.0.0":                 false,
		"3.1.277":               false,
		"not a version":         false,
		"":                      false,
	} {
		if got := supportsSkillListing(version); got != want {
			t.Errorf("supportsSkillListing(%q) = %v, want %v", version, got, want)
		}
	}
}

// fixtureCommands decodes a captured initialize reply's commands the way a
// reader who trusts nothing in list.go would: generically, builtin rows and
// all.
func fixtureCommands(t *testing.T, name string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var line struct {
		Response struct {
			Response struct {
				Commands []map[string]any `json:"commands"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(b, &line); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return line.Response.Response.Commands
}

// fixtureInitialize reads a captured reply through the exchange's own reader.
// The captures were made with request_id "r1".
func fixtureInitialize(t *testing.T, name string) []slashCommand {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	commands, err := readInitialize(bytes.NewReader(b), "r1")
	if err != nil {
		t.Fatalf("readInitialize(%s): %v", name, err)
	}
	return commands
}

// TestInitializeFixtures decodes both 2.1.277 captures (task 124.7): every
// non-builtin entry in the CLI's order, descriptions verbatim with their
// labels, argument hints and aliases kept, and Scope, Plugin and Path left
// empty. Nothing from the reply's account or models reaches the value.
func TestInitializeFixtures(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		// want holds rows the capture's setup put there on purpose: a
		// project skill with an argument hint, a legacy `.claude/commands/`
		// file, a `disable-model-invocation` skill and a plugin skill.
		want []agent.Skill
	}{
		{
			fixture: "initialize_2.1.277.jsonl",
			want: []agent.Skill{
				{Name: "manual-only", Description: "A skill only a human may invoke. (project)"},
				{Name: "proj-skill", Description: "Project skill for discovery test. Use when testing. (project)", ArgumentHint: "[issue-number] [priority]"},
				{Name: "proj-cmd", Description: "A legacy custom command (project)", ArgumentHint: "<file>"},
			},
		},
		{
			fixture: "initialize_loggedout_2.1.277.jsonl",
			want: []agent.Skill{
				{Name: "manual-only", Description: "A skill only a human may invoke. (project)"},
				{Name: "proj-skill", Description: "Project skill for discovery test. Use when testing. (project)", ArgumentHint: "[issue-number] [priority]"},
				{Name: "proj-cmd", Description: "A legacy custom command (project)", ArgumentHint: "<file>"},
				{Name: "demo:greet", Description: "(demo) Greets whoever asks. Use when testing plugin skills.", ArgumentHint: "[name]", Aliases: []string{"greet"}},
			},
		},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			got := skillsFrom(fixtureInitialize(t, tc.fixture))

			raw := fixtureCommands(t, tc.fixture)
			var wantNames, builtins []string
			var sawBuiltinAlias, sawPluginAlias bool
			for _, c := range raw {
				name, _ := c["name"].(string)
				_, aliased := c["aliases"]
				if c["builtin"] == true {
					builtins = append(builtins, name)
					sawBuiltinAlias = sawBuiltinAlias || aliased
					continue
				}
				sawPluginAlias = sawPluginAlias || (aliased && strings.Contains(name, ":"))
				wantNames = append(wantNames, name)
			}
			if len(builtins) == 0 || !sawBuiltinAlias || !sawPluginAlias {
				t.Fatalf("the capture no longer holds a builtin row, an aliased builtin and an aliased plugin row (%d builtins)", len(builtins))
			}
			var gotNames []string
			for _, s := range got.Skills {
				gotNames = append(gotNames, s.Name)
				if s.Scope != "" || s.Plugin != "" || s.Path != "" {
					t.Errorf("%s: Scope %q, Plugin %q, Path %q, want all empty (nothing synthesized)", s.Name, s.Scope, s.Plugin, s.Path)
				}
			}
			if !slices.Equal(gotNames, wantNames) {
				t.Errorf("names = %q\nwant the capture's non-builtin rows in order: %q", gotNames, wantNames)
			}
			for _, b := range builtins {
				if slices.Contains(gotNames, b) {
					t.Errorf("builtin %q was listed (task 124 decision 7)", b)
				}
			}
			for _, w := range tc.want {
				i := slices.IndexFunc(got.Skills, func(s agent.Skill) bool { return s.Name == w.Name })
				if i < 0 {
					t.Errorf("%s is not listed", w.Name)
				} else if !reflect.DeepEqual(got.Skills[i], w) {
					t.Errorf("%s = %+v, want %+v", w.Name, got.Skills[i], w)
				}
			}
			if slices.Contains(gotNames, "hidden-skill") {
				t.Error("a `user-invocable: false` skill was listed; the CLI omits it")
			}
			if got.Problems != nil {
				t.Errorf("Problems = %v, want nil: claude reports none", got.Problems)
			}
			dump := fmt.Sprintf("%+v", got)
			for _, leak := range []string{"user@example.com", "Example Organization", "tokenSource", "resolvedModel", "subscriptionType"} {
				if strings.Contains(dump, leak) {
					t.Errorf("the decoded list holds %q from the reply's account or models", leak)
				}
			}
		})
	}
	// The logged-in capture's plugin skill comes from an installed plugin.
	skills := skillsFrom(fixtureInitialize(t, "initialize_2.1.277.jsonl")).Skills
	i := slices.IndexFunc(skills, func(s agent.Skill) bool { return s.Name == "caveman:caveman" })
	if i < 0 || !slices.Equal(skills[i].Aliases, []string{"caveman"}) {
		t.Errorf("caveman:caveman is missing or its aliases are not [caveman]")
	}
}

// TestReadInitialize pins the exchange's reader on constructed streams: which
// lines it skips, what only-`commands` decoding tolerates, and which replies
// are errors rather than lists (task 124 decision 16).
func TestReadInitialize(t *testing.T) {
	const cmd = `{"name":"a","description":"A (user)","argumentHint":"[x]"}`
	reply := func(inner string) string {
		return `{"type":"control_response","response":{"subtype":"success","request_id":"id","response":` + inner + `}}`
	}
	tests := []struct {
		name    string
		stream  []string
		want    []string // names; nil with wantErr
		wantErr string
	}{
		{
			name: "skips every line that is not the matching reply",
			stream: []string{
				`{"type":"system","subtype":"hook_started","hook_name":"SessionStart"}`,
				"",
				`{"type":"control_response","response":{"subtype":"success","request_id":"other","response":{"commands":[{"name":"wrong"}]}}}`,
				`{"type":"something_new","response":"a string"}`,
				`[1,2,3]`,
				reply(`{"commands":[` + cmd + `]}`),
			},
			want: []string{"a"},
		},
		{
			name:   "an account of a hostile shape is never decoded",
			stream: []string{reply(`{"account":[1,2],"models":"x","agents":7,"commands":[` + cmd + `]}`)},
			want:   []string{"a"},
		},
		{
			name:   "a numeric account too",
			stream: []string{reply(`{"commands":[` + cmd + `],"account":5}`)},
			want:   []string{"a"},
		},
		{
			name:   "repeated names are kept",
			stream: []string{reply(`{"commands":[` + cmd + `,` + cmd + `]}`)},
			want:   []string{"a", "a"},
		},
		{
			name:   "an empty list is a list",
			stream: []string{reply(`{"commands":[]}`)},
			want:   []string{},
		},
		{name: "a reply without commands", stream: []string{reply(`{"account":{}}`)}, wantErr: "no commands"},
		{name: "null commands", stream: []string{reply(`{"commands":null}`)}, wantErr: "no commands"},
		{name: "a null response", stream: []string{reply(`null`)}, wantErr: "no commands"},
		{
			name:    "an error reply",
			stream:  []string{`{"type":"control_response","response":{"subtype":"error","request_id":"id","error":"nope"}}`},
			wantErr: "refused initialize: nope",
		},
		{
			name:    "an unknown subtype",
			stream:  []string{`{"type":"control_response","response":{"subtype":"maybe","request_id":"id"}}`},
			wantErr: `subtype "maybe"`,
		},
		{name: "commands of the wrong shape", stream: []string{reply(`{"commands":"all of them"}`)}, wantErr: "decode initialize reply"},
		{name: "a line that is not JSON", stream: []string{`{"type":"system"}`, `not json {`}, wantErr: "malformed line"},
		{name: "a stream that ends first", stream: []string{`{"type":"system","subtype":"hook_started"}`}, wantErr: errNoReply.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands, err := readInitialize(strings.NewReader(strings.Join(tt.stream, "\n")+"\n"), "id")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				if errors.Is(err, agent.ErrSkillsUnsupported) {
					t.Errorf("err %v is ErrSkillsUnsupported; only a build outside the floor is (decision 16)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("readInitialize: %v", err)
			}
			got := skillsFrom(commands)
			names := []string{}
			for _, s := range got.Skills {
				names = append(names, s.Name)
			}
			if !slices.Equal(names, tt.want) {
				t.Errorf("names = %q, want %q", names, tt.want)
			}
			if got.Skills == nil {
				t.Error("Skills = nil for a reply that answered; want a non-nil list")
			}
		})
	}
}

// TestReadInitializeTakesALongLine proves the scanner is raised past bufio's
// default token: the real reply is one line of tens of kilobytes.
func TestReadInitializeTakesALongLine(t *testing.T) {
	desc := strings.Repeat("d", 200*1024)
	line := `{"type":"control_response","response":{"subtype":"success","request_id":"id","response":{"commands":[{"name":"big","description":"` + desc + `"}]}}}`
	commands, err := readInitialize(strings.NewReader(line+"\n"), "id")
	if err != nil {
		t.Fatalf("readInitialize: %v", err)
	}
	if len(commands) != 1 || commands[0].Description != desc {
		t.Fatalf("got %d commands, want the one long one", len(commands))
	}
}

// fakeListing points claude at fakeagent and records its argv. version ""
// keeps the fake's default, which is below the floor.
func fakeListing(t *testing.T, version string) (a *Adapter, argvFile string) {
	t.Helper()
	path := agenttest.BuildFakeAgent(t)
	if version != "" {
		// The version probe inherits the process environment, never
		// SkillQuery.Env, so the version is set where it will look.
		t.Setenv("FAKEAGENT_VERSION", version)
	}
	argvFile = filepath.Join(t.TempDir(), "argv.log")
	t.Setenv("FAKEAGENT_ARGV_FILE", argvFile)
	return New(func() string { return path }), argvFile
}

func argvLines(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")), "\n")
}

// TestListSkillsAgainstTheFake is the whole exchange against a real process:
// the version probe, then one spawn carrying exactly listSkillsArgs, whose
// reply loses its built-in row and keeps the rest verbatim.
func TestListSkillsAgainstTheFake(t *testing.T) {
	a, argvFile := fakeListing(t, "2.1.277")
	if !agent.CanListSkills(a) {
		t.Fatal("CanListSkills(claude) = false, want true")
	}
	got, err := a.ListSkills(t.Context(), agent.SkillQuery{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	want := agent.SkillList{Skills: []agent.Skill{
		{Name: "fake-skill", Description: "A fake project skill. (project)", ArgumentHint: "[target]"},
		{Name: "fake:tool", Description: "(fake) A fake plugin skill.", Aliases: []string{"tool"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListSkills = %+v\nwant %+v", got, want)
	}
	lines := argvLines(t, argvFile)
	if len(lines) != 2 || lines[0] != "--version" {
		t.Fatalf("argv lines = %q, want the version probe then the listing", lines)
	}
	argv := lines[1]
	if argv != strings.Join(listSkillsArgs(), " ") {
		t.Errorf("listing argv = %q, want %q", argv, strings.Join(listSkillsArgs(), " "))
	}
	for _, flag := range []string{"--strict-mcp-config", `--settings {"disableAllHooks":true}`, "--input-format stream-json", "--verbose"} {
		if !strings.Contains(argv, flag) {
			t.Errorf("listing argv %q lacks %s", argv, flag)
		}
	}
	for _, flag := range []string{
		"--resume", "--bare", "--model", "--effort", "--permission-prompt-tool", "--allowedTools",
		"--dangerously-skip-permissions", "--mcp-config", "--replay-user-messages",
	} {
		if slices.Contains(strings.Fields(argv), flag) {
			t.Errorf("listing argv %q carries %s (task 124 decision 41)", argv, flag)
		}
	}
}

// TestListSkillsRunsInTheWorkDir proves the probe starts where the next run
// would: the fake reports its working directory as one entry's description.
func TestListSkillsRunsInTheWorkDir(t *testing.T) {
	a, _ := fakeListing(t, "2.1.277")
	dir := t.TempDir()
	got, err := a.ListSkills(t.Context(), agent.SkillQuery{
		WorkDir: dir,
		Env:     append(os.Environ(), "FAKEAGENT_SKILLS_ECHO_CWD=1"),
	})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if n := len(got.Skills); n == 0 || got.Skills[n-1].Name != "fake-cwd" {
		t.Fatalf("ListSkills = %+v, want the fake's cwd entry last", got)
	}
	// macOS temp dirs are symlinks: compare what both resolve to.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	gotDir, err := filepath.EvalSymlinks(got.Skills[len(got.Skills)-1].Description)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != want {
		t.Errorf("the listing ran in %q, want %q", gotDir, want)
	}
}

// TestListSkillsGoesThroughTheLauncher proves q.Launcher resolves the binary,
// answers the version probe and receives the one Launch, with a retained
// stdin carrying exactly the initialize request.
func TestListSkillsGoesThroughTheLauncher(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	rec := &agenttest.RecordingLauncher{
		OnResolve: func(adapter, configured, binary string) (string, error) {
			if adapter != "claude" || binary != binaryName {
				return "", fmt.Errorf("resolve(%q, %q, %q)", adapter, configured, binary)
			}
			return fake, nil
		},
		// The host's fake would answer 2.1.224; the launcher's CLI is the
		// one that decides.
		OnProbe: func(string, ...string) ([]byte, []byte, error) {
			return []byte("2.1.277 (Claude Code)\n"), nil, nil
		},
	}
	q := agent.SkillQuery{WorkDir: t.TempDir(), Launcher: rec, Env: os.Environ()}
	got, err := New(nil).ListSkills(t.Context(), q)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(got.Skills) != 2 {
		t.Errorf("ListSkills = %+v, want the fake's two non-builtin rows", got)
	}
	if probes := rec.Probes(); !reflect.DeepEqual(probes, [][]string{{fake, "--version"}}) {
		t.Errorf("probes = %q, want one --version of the launcher-resolved binary", probes)
	}
	launches := rec.Launches()
	if len(launches) != 1 {
		t.Fatalf("launcher saw %d launches, want 1", len(launches))
	}
	c := launches[0].Command
	if c.Path != fake || !slices.Equal(c.Args, listSkillsArgs()) || c.Dir != q.WorkDir {
		t.Errorf("Command = %q %q in %q, want %q %q in %q", c.Path, c.Args, c.Dir, fake, listSkillsArgs(), q.WorkDir)
	}
	if !slices.Equal(c.Env, q.Env) {
		t.Error("Command.Env is not SkillQuery.Env")
	}
	if !c.StdinPipe || c.Stderr == nil {
		t.Errorf("StdinPipe = %v, Stderr set = %v, want both", c.StdinPipe, c.Stderr != nil)
	}
	if stdin := launches[0].Stdin(); string(stdin) != string(initializeLine()) {
		t.Errorf("stdin = %q, want exactly %q", stdin, initializeLine())
	}
}

// TestListSkillsBelowOrPastTheFloor is decision 40's refusal: a build outside
// [2.1.277, 3.0.0) is ErrSkillsUnsupported, names its version, and is never
// asked — only the --version probe ran.
func TestListSkillsBelowOrPastTheFloor(t *testing.T) {
	for _, tc := range []struct{ set, reported string }{
		{"2.1.276", "2.1.276"},
		{"", defaultFakeVersion},
		{"3.0.0", "3.0.0"},
	} {
		t.Run("version "+tc.reported, func(t *testing.T) {
			a, argvFile := fakeListing(t, tc.set)
			got, err := a.ListSkills(t.Context(), agent.SkillQuery{WorkDir: t.TempDir()})
			if !errors.Is(err, agent.ErrSkillsUnsupported) {
				t.Fatalf("err = %v, want ErrSkillsUnsupported", err)
			}
			if !strings.Contains(err.Error(), tc.reported) || !strings.Contains(err.Error(), skillListingFloor) {
				t.Errorf("err %q names neither the version %s nor the floor %s", err, tc.reported, skillListingFloor)
			}
			if !reflect.DeepEqual(got, agent.SkillList{}) {
				t.Errorf("list = %+v, want empty", got)
			}
			if lines := argvLines(t, argvFile); !slices.Equal(lines, []string{"--version"}) {
				t.Errorf("argv lines = %q, want only the version probe", lines)
			}
		})
	}
}

// defaultFakeVersion is cmd/fakeagent's unset claude version, below the
// listing floor on purpose: many version tests depend on it staying put.
const defaultFakeVersion = "2.1.224"

// TestListSkillsFailuresAreUnknown is decision 16's other half: every way the
// probe can fail is an ordinary error, never ErrSkillsUnsupported, with no
// list and with the CLI's stderr tail where it wrote one.
func TestListSkillsFailuresAreUnknown(t *testing.T) {
	restore := skillListTimeout
	skillListTimeout = 2 * time.Second
	t.Cleanup(func() { skillListTimeout = restore })
	for _, tc := range []struct{ mode, want string }{
		{"hang", "no initialize reply within"},
		{"error", "refused initialize: fakeagent refused initialize"},
		{"malformed", "malformed line"},
		{"exit", "initialize failed on purpose"},
		{"nocommands", "no commands"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			a, _ := fakeListing(t, "2.1.277")
			got, err := a.ListSkills(t.Context(), agent.SkillQuery{
				WorkDir: t.TempDir(),
				Env:     append(os.Environ(), "FAKEAGENT_CLAUDE_INITIALIZE="+tc.mode),
			})
			if err == nil {
				t.Fatalf("ListSkills = %+v, want an error", got)
			}
			if errors.Is(err, agent.ErrSkillsUnsupported) {
				t.Errorf("err %v is ErrSkillsUnsupported, want an ordinary error (decision 16)", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want one containing %q", err, tc.want)
			}
			if !reflect.DeepEqual(got, agent.SkillList{}) {
				t.Errorf("list = %+v, want empty", got)
			}
		})
	}
}

// TestListSkillsKeepsAnAnswerFromALingeringCLI: a CLI that answered and then
// outlived the bound is killed, and its answer stands.
func TestListSkillsKeepsAnAnswerFromALingeringCLI(t *testing.T) {
	restore := skillListTimeout
	skillListTimeout = time.Second
	t.Cleanup(func() { skillListTimeout = restore })
	a, _ := fakeListing(t, "2.1.277")
	got, err := a.ListSkills(t.Context(), agent.SkillQuery{
		WorkDir: t.TempDir(),
		Env:     append(os.Environ(), "FAKEAGENT_CLAUDE_INITIALIZE=linger"),
	})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(got.Skills) != 2 {
		t.Errorf("ListSkills = %+v, want the fake's two non-builtin rows", got)
	}
}

// TestListSkillsHonorsCancel: a canceled context kills the probe well inside
// the exchange's own bound.
func TestListSkillsHonorsCancel(t *testing.T) {
	a, _ := fakeListing(t, "2.1.277")
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, err := a.ListSkills(ctx, agent.SkillQuery{
		WorkDir: t.TempDir(),
		Env:     append(os.Environ(), "FAKEAGENT_CLAUDE_INITIALIZE=hang"),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > skillListTimeout/2 {
		t.Errorf("ListSkills returned after %s; cancel did not kill the probe", elapsed)
	}
}

// TestListSkillsUnprobeableBinary: a binary that cannot be resolved, or whose
// --version fails, is "nobody can say" — never the positive no.
func TestListSkillsUnprobeableBinary(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "claude-not-here")
	_, err := New(func() string { return missing }).ListSkills(t.Context(), agent.SkillQuery{WorkDir: t.TempDir()})
	if err == nil || errors.Is(err, agent.ErrSkillsUnsupported) {
		t.Errorf("unresolvable binary: err = %v, want an ordinary error", err)
	}

	fake := agenttest.BuildFakeAgent(t)
	rec := &agenttest.RecordingLauncher{
		OnProbe: func(string, ...string) ([]byte, []byte, error) {
			return nil, []byte("boom"), errors.New("exit status 1")
		},
	}
	_, err = New(func() string { return fake }).ListSkills(t.Context(), agent.SkillQuery{WorkDir: t.TempDir(), Launcher: rec})
	if err == nil || errors.Is(err, agent.ErrSkillsUnsupported) {
		t.Errorf("failed --version: err = %v, want an ordinary error", err)
	}
	if n := len(rec.Launches()); n != 0 {
		t.Errorf("launcher saw %d launches after a failed version probe, want 0", n)
	}
}
