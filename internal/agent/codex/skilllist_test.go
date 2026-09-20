package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// skillsFixture returns the `result` payload of the captured 0.154.0
// `skills/list` response, as raw bytes off disk.
func skillsFixture(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "app_server_skills_0.154.0.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatalf("parse fixture envelope: %v", err)
	}
	return envelope.Result
}

// fixtureEntry is the fixture's one `data` entry, decoded loosely so a test
// can compare the mapping against the bytes codex sent rather than against a
// retyped copy of them.
type fixtureEntry struct {
	Cwd    string            `json:"cwd"`
	Skills []json.RawMessage `json:"skills"`
	Errors []json.RawMessage `json:"errors"`
}

// rawSkillRow is one captured `SkillMetadata`, as codex sent it.
type rawSkillRow struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Path        string  `json:"path"`
	Scope       string  `json:"scope"`
	Enabled     bool    `json:"enabled"`
	PluginID    *string `json:"pluginId"`
}

// sentMessage is one JSON-RPC line the adapter wrote to the app-server.
type sentMessage struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func decodeFixture(t *testing.T, result json.RawMessage) fixtureEntry {
	t.Helper()
	var r struct {
		Data []fixtureEntry `json:"data"`
	}
	if err := json.Unmarshal(result, &r); err != nil || len(r.Data) != 1 {
		t.Fatalf("fixture result: %v (%d entries)", err, len(r.Data))
	}
	return r.Data[0]
}

func TestCodexCanListSkills(t *testing.T) {
	if !agent.CanListSkills(New(nil)) {
		t.Fatal("CanListSkills(codex) = false, want true")
	}
}

// TestParseSkillsList holds the mapping to the captured 0.154.0 answer: the
// enabled rows in codex's order with every field verbatim, the disabled row
// gone, and the broken SKILL.md surfaced as a problem rather than dropped.
func TestParseSkillsList(t *testing.T) {
	result := skillsFixture(t)
	got, err := parseSkillsList(result)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// The capture's legs, in codex's own order: .codex/skills, then
	// .agents/skills, then the user skill repeating a repo skill's name, then
	// the bundled system skills. release-notes sits between the first two in
	// the capture, disabled through `skills.config`.
	type row struct{ name, scope string }
	want := []row{
		{"codex-dir-skill", "repo"},
		{"review", "repo"},
		{"review", "user"},
		{"imagegen", "system"},
		{"openai-docs", "system"},
		{"plugin-creator", "system"},
		{"review-agent", "system"},
		{"skill-creator", "system"},
		{"skill-installer", "system"},
	}
	if len(got.Skills) != len(want) {
		t.Fatalf("skills = %+v, want %d rows", got.Skills, len(want))
	}
	for i, w := range want {
		if s := got.Skills[i]; s.Name != w.name || s.Scope != w.scope {
			t.Errorf("skill %d = %s/%s, want %s/%s", i, s.Name, s.Scope, w.name, w.scope)
		}
	}

	// Every mapped field against the raw row it came from: the enabled rows
	// are the captured rows minus the disabled one, so the i-th skill pairs
	// with the i-th raw row whose `enabled` is not false.
	var raw []rawSkillRow
	for _, r := range decodeFixture(t, result).Skills {
		var row rawSkillRow
		if err := json.Unmarshal(r, &row); err != nil {
			t.Fatalf("raw row: %v", err)
		}
		if row.Name == "release-notes" {
			if row.Enabled {
				t.Fatal("the capture's release-notes row must be the disabled one")
			}
			continue
		}
		raw = append(raw, row)
	}
	for i, r := range raw {
		s := got.Skills[i]
		if s.Description != r.Description || s.Path != r.Path || s.Scope != r.Scope {
			t.Errorf("skill %d = %+v, want the raw row %+v verbatim", i, s, r)
		}
		if s.Description == "" || s.Path == "" {
			t.Errorf("skill %d lost its description or path: %+v", i, s)
		}
		if s.Plugin != "" || r.PluginID != nil {
			t.Errorf("skill %d plugin = %q, raw pluginId %v; the capture has no plugin skill", i, s.Plugin, r.PluginID)
		}
		if s.ArgumentHint != "" || s.Aliases != nil {
			t.Errorf("skill %d carries an argument hint or aliases codex never reports: %+v", i, s)
		}
	}
	if got.Skills[0].Description != "Lives only in .codex/skills." {
		t.Errorf("description = %q", got.Skills[0].Description)
	}
	if !strings.Contains(got.Skills[1].Path, "Application Support") {
		t.Errorf("path = %q; the capture's worktree lives under a path with a space", got.Skills[1].Path)
	}
	for _, s := range got.Skills {
		if s.Name == "release-notes" {
			t.Errorf("the disabled row survived: %+v", s)
		}
	}

	wantProblems := []agent.SkillProblem{{
		Path:    "/Users/you/Library/Application Support/vincent/data/worktrees/7/.agents/skills/broken/SKILL.md",
		Message: "missing YAML frontmatter delimited by ---",
	}}
	if !reflect.DeepEqual(got.Problems, wantProblems) {
		t.Errorf("problems = %+v, want %+v", got.Problems, wantProblems)
	}
}

// TestParseSkillsListPluginAndEmpty covers the two legs the capture cannot:
// a plugin skill's `pluginId` (no plugin loads in an isolated CODEX_HOME), and
// a well-formed answer with nothing in it. Both are derived from the captured
// bytes rather than typed out.
func TestParseSkillsListPluginAndEmpty(t *testing.T) {
	entry := decodeFixture(t, skillsFixture(t))

	var row map[string]json.RawMessage
	if err := json.Unmarshal(entry.Skills[0], &row); err != nil {
		t.Fatalf("raw row: %v", err)
	}
	row["name"] = json.RawMessage(`"pdf:pdf"`)
	row["pluginId"] = json.RawMessage(`"pdf@openai-primary-runtime"`)
	pluginRow, _ := json.Marshal(row)
	withPlugin, _ := json.Marshal(map[string]any{"data": []any{map[string]any{
		"cwd": entry.Cwd, "skills": []json.RawMessage{pluginRow}, "errors": []any{},
	}}})
	got, err := parseSkillsList(withPlugin)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.Skills) != 1 || got.Skills[0].Name != "pdf:pdf" || got.Skills[0].Plugin != "pdf@openai-primary-runtime" {
		t.Errorf("skills = %+v, want pdf:pdf with its pluginId as Plugin", got.Skills)
	}
	if got.Problems != nil {
		t.Errorf("problems = %+v, want nil when codex reported none", got.Problems)
	}

	empty, _ := json.Marshal(map[string]any{"data": []any{map[string]any{
		"cwd": entry.Cwd, "skills": []any{}, "errors": []any{},
	}}})
	got, err = parseSkillsList(empty)
	if err != nil {
		t.Fatalf("an empty listing is a listing, not an error: %v", err)
	}
	if len(got.Skills) != 0 || got.Problems != nil {
		t.Errorf("empty listing = %+v", got)
	}
}

// TestParseSkillsListMalformed pins that an answer vincent cannot read is an
// ordinary error — "nobody can say" — and never the positive no.
func TestParseSkillsListMalformed(t *testing.T) {
	entry := decodeFixture(t, skillsFixture(t))
	one, _ := json.Marshal(entry)
	for _, tc := range []struct {
		name   string
		result string
	}{
		{"zero entries", `{"data":[]}`},
		{"no data at all", `{}`},
		{"two entries", `{"data":[` + string(one) + `,` + string(one) + `]}`},
		{"not an object", `"nonsense"`},
		{"data is not a list", `{"data":"nonsense"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSkillsList(json.RawMessage(tc.result))
			if err == nil {
				t.Fatalf("want an error, got %+v", got)
			}
			if errors.Is(err, agent.ErrSkillsUnsupported) {
				t.Fatalf("error %v reads as unsupported; a malformed answer is unknown", err)
			}
			if got.Skills != nil || got.Problems != nil {
				t.Fatalf("a failed parse must yield no listing, got %+v", got)
			}
		})
	}
}

// seedAgentsSkill writes one `.agents/skills/<dir>/SKILL.md` under root, the
// layout the fake app-server derives its default list from.
func seedAgentsSkill(t *testing.T, root, dir, name, description string) string {
	t.Helper()
	d := filepath.Join(root, ".agents", "skills", dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "SKILL.md")
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nDo it.\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestListSkillsHandsTheLauncherItsSpawn pins the spawn (decision 34): one
// `app-server --stdio` through the query's launcher, in its directory with
// its environment and a retained stdin, and the request codex is asked —
// `skills/list` for exactly the worktree, reloaded — after the handshake.
func TestListSkillsHandsTheLauncherItsSpawn(t *testing.T) {
	bin := agenttest.BuildFakeAgent(t)
	workDir := filepath.Join(t.TempDir(), "with space")
	skillPath := seedAgentsSkill(t, workDir, "review", "review", "Review the change.")
	env := append(os.Environ(), "FAKEAGENT_CODEX_APP_SERVER=healthy")
	rec := &agenttest.RecordingLauncher{}

	got, err := New(func() string { return bin }).ListSkills(t.Context(), agent.SkillQuery{
		WorkDir: workDir, Launcher: rec, Env: env,
	})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	want := []agent.Skill{{Name: "review", Description: "Review the change.", Scope: "repo", Path: skillPath}}
	if !reflect.DeepEqual(got.Skills, want) {
		t.Errorf("skills = %+v, want %+v", got.Skills, want)
	}

	launches := rec.Launches()
	if len(launches) != 1 {
		t.Fatalf("launcher saw %d commands, want 1", len(launches))
	}
	cmd := launches[0].Command
	if cmd.Path != bin || !slices.Equal(cmd.Args, []string{"app-server", "--stdio"}) {
		t.Errorf("command = %q %q, want %q app-server --stdio", cmd.Path, cmd.Args, bin)
	}
	if cmd.Dir != workDir {
		t.Errorf("Dir = %q, want %q", cmd.Dir, workDir)
	}
	if !slices.Equal(cmd.Env, env) {
		t.Error("Env is not the query's environment")
	}
	if !cmd.StdinPipe {
		t.Error("StdinPipe = false; the exchange writes after launch")
	}
	if cmd.Stderr == nil {
		t.Error("Stderr is nil; the failure line would be lost")
	}

	var sent []sentMessage
	sc := bufio.NewScanner(bytes.NewReader(launches[0].Stdin()))
	for sc.Scan() {
		var m sentMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("stdin line %q: %v", sc.Text(), err)
		}
		sent = append(sent, m)
	}
	var methods []string
	for _, m := range sent {
		methods = append(methods, m.Method)
	}
	if !slices.Equal(methods, []string{"initialize", "initialized", "skills/list"}) {
		t.Fatalf("stdin methods = %q, want the handshake then skills/list", methods)
	}
	var params struct {
		Cwds        []string `json:"cwds"`
		ForceReload *bool    `json:"forceReload"`
	}
	if err := json.Unmarshal(sent[2].Params, &params); err != nil {
		t.Fatalf("skills/list params: %v", err)
	}
	if !slices.Equal(params.Cwds, []string{workDir}) {
		t.Errorf("cwds = %q, want [%q]", params.Cwds, workDir)
	}
	if params.ForceReload == nil || !*params.ForceReload {
		t.Errorf("forceReload = %v, want true", params.ForceReload)
	}
}

// TestListSkillsAgainstFakeAgent drives the adapter through the fake
// app-server on the host (a nil launcher and the daemon's environment), over
// every mode the fake answers with.
func TestListSkillsAgainstFakeAgent(t *testing.T) {
	bin := agenttest.BuildFakeAgent(t)
	workDir := t.TempDir()
	skillPath := seedAgentsSkill(t, workDir, "review", "review", "Review the change.")
	list := func(t *testing.T, ctx context.Context, path string) (agent.SkillList, error) {
		t.Helper()
		return New(func() string { return path }).ListSkills(ctx, agent.SkillQuery{WorkDir: workDir})
	}
	derived := []agent.Skill{{Name: "review", Description: "Review the change.", Scope: "repo", Path: skillPath}}

	t.Run("a nil launcher runs on the host and derives from .agents/skills", func(t *testing.T) {
		t.Setenv("FAKEAGENT_CODEX_APP_SERVER", "healthy")
		got, err := list(t, t.Context(), bin)
		if err != nil {
			t.Fatalf("ListSkills: %v", err)
		}
		if !reflect.DeepEqual(got.Skills, derived) || got.Problems != nil {
			t.Errorf("listing = %+v, want %+v", got, derived)
		}
	})

	t.Run("an unauthenticated account still lists", func(t *testing.T) {
		t.Setenv("FAKEAGENT_CODEX_APP_SERVER", "unauthenticated")
		got, err := list(t, t.Context(), bin)
		if err != nil {
			t.Fatalf("ListSkills: %v", err)
		}
		if !reflect.DeepEqual(got.Skills, derived) {
			t.Errorf("skills = %+v, want %+v", got.Skills, derived)
		}
	})

	t.Run("FAKEAGENT_CODEX_SKILLS is used verbatim", func(t *testing.T) {
		t.Setenv("FAKEAGENT_CODEX_APP_SERVER", "healthy")
		t.Setenv("FAKEAGENT_CODEX_SKILLS", `[
			{"name":"pdf:pdf","description":"Read PDFs.","path":"/p/pdf/SKILL.md","scope":"user","enabled":true,"pluginId":"pdf@openai-primary-runtime"},
			{"name":"off","description":"Disabled.","path":"/p/off/SKILL.md","scope":"repo","enabled":false,"pluginId":null},
			{"name":"imagegen","description":"Images.","path":"/p/imagegen/SKILL.md","scope":"system","enabled":true,"pluginId":null}
		]`)
		got, err := list(t, t.Context(), bin)
		if err != nil {
			t.Fatalf("ListSkills: %v", err)
		}
		want := []agent.Skill{
			{Name: "pdf:pdf", Description: "Read PDFs.", Path: "/p/pdf/SKILL.md", Scope: "user", Plugin: "pdf@openai-primary-runtime"},
			{Name: "imagegen", Description: "Images.", Path: "/p/imagegen/SKILL.md", Scope: "system"},
		}
		if !reflect.DeepEqual(got.Skills, want) {
			t.Errorf("skills = %+v, want %+v", got.Skills, want)
		}
		// `skills/list` has no `builtin` field and nothing synthesizes one
		// (task 124.16): every row codex reports is an ordinary skill, which
		// is why a codex chat is never told about bundled ones.
		for _, s := range got.Skills {
			if s.Builtin {
				t.Errorf("%s came back Builtin; codex has no such concept", s.Name)
			}
		}
	})

	for _, tc := range []struct {
		name    string
		mode    string
		path    func(t *testing.T) string
		timeout time.Duration
		wantErr string
	}{
		{name: "a JSON-RPC error reply", mode: "error", wantErr: "failed to list skills"},
		{name: "an answer that does not parse", mode: "malformed", wantErr: "parse codex skills"},
		{
			name: "a handshake that never completes",
			mode: "hang",
			// A caller's deadline cuts in well under appServerTimeout, so
			// this leg costs a second rather than ten.
			timeout: time.Second,
			wantErr: "context deadline exceeded",
		},
		{
			name:    "a binary that is not there",
			path:    func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-such-codex") },
			wantErr: "configured codex path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKEAGENT_CODEX_APP_SERVER", tc.mode)
			path := bin
			if tc.path != nil {
				path = tc.path(t)
			}
			ctx := t.Context()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			got, err := list(t, ctx, path)
			if err == nil {
				t.Fatalf("want an error, got %+v", got)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			if errors.Is(err, agent.ErrSkillsUnsupported) {
				t.Fatalf("error %v reads as unsupported; a failed probe is unknown", err)
			}
			if got.Skills != nil || got.Problems != nil {
				t.Fatalf("a failed probe must yield no listing, got %+v", got)
			}
		})
	}
}
