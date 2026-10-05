package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestResolveStartupProject is task 132.3's chain, table-driven: each rule
// winning on its own, each losing to the rule above it, and each fallthrough
// naming what failed and what won.
func TestResolveStartupProject(t *testing.T) {
	root := t.TempDir()
	dir := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	api := apiclient.Project{ID: 1, Name: "api", Path: dir("repos", "api")}
	web := apiclient.Project{ID: 2, Name: "web", Path: dir("repos", "web")}
	webapp := apiclient.Project{ID: 3, Name: "webapp", Path: dir("repos", "webapp")}
	// home is a project whose path is a parent of the data dir, the way a
	// project at ~ would be.
	home := apiclient.Project{ID: 4, Name: "home", Path: root}
	numeric := apiclient.Project{ID: 9, Name: "1"}
	nested := apiclient.Project{ID: 5, Name: "nested", Path: dir("repos", "web", "packages", "ui")}
	all := []apiclient.Project{api, web, webapp}
	wt := dir("data", "worktrees", "17")
	task := apiclient.Task{ID: 17, ProjectID: api.ID, WorktreePath: &wt}
	chatWT := dir("data", "worktrees", "chat-3")
	chat := apiclient.Chat{ID: 3, ProjectID: web.ID, WorktreePath: chatWT}

	cases := []struct {
		name       string
		in         startupInputs
		want       string
		why        string
		noticeHas  []string
		silent     bool
		noProjects bool
	}{
		{name: "flag by name", in: startupInputs{flag: "web", cwd: api.Path, defaultProject: "api", projects: all}, want: "web", why: whyFlag, silent: true},
		{name: "flag by id", in: startupInputs{flag: "3", projects: all}, want: "webapp", why: whyFlag, silent: true},
		{name: "flag name beats id", in: startupInputs{flag: "1", projects: append([]apiclient.Project{numeric}, all...)}, want: "1", why: whyFlag},
		{name: "flag id when no name matches", in: startupInputs{flag: "1", projects: all}, want: "api", why: whyFlag},
		{name: "flag signed is a name", in: startupInputs{flag: "+1", projects: all, lastUsed: &selectedProjectState{ID: 2, Name: "web"}}, want: "web", why: whyLastUsed},
		{
			name: "unknown flag falls through to cwd",
			in:   startupInputs{flag: "nope", cwd: api.Path, projects: all},
			want: "api", why: whyCwd,
			noticeHas: []string{"--project `nope` is not registered", "showing `api` (from the working directory)"},
		},
		{name: "cwd in a project subdirectory", in: startupInputs{cwd: dir("repos", "web", "src"), defaultProject: "api", projects: all}, want: "web", why: whyCwd, noticeHas: []string{"◆ web — from the working directory"}},
		{name: "cwd by component", in: startupInputs{cwd: dir("repos", "webapp", "x"), projects: []apiclient.Project{api, web, webapp}}, want: "webapp", why: whyCwd},
		{name: "deepest project wins", in: startupInputs{cwd: dir("repos", "web", "packages", "ui", "src"), projects: []apiclient.Project{web, nested}}, want: "nested", why: whyCwd},
		{name: "deepest wins in either order", in: startupInputs{cwd: dir("repos", "web", "packages", "ui"), projects: []apiclient.Project{nested, web}}, want: "nested", why: whyCwd},
		{name: "task worktree beats a parent project", in: startupInputs{cwd: filepath.Join(wt, "internal"), projects: []apiclient.Project{home, api, web}, tasks: []apiclient.Task{task}}, want: "api", why: whyCwd},
		{name: "chat worktree", in: startupInputs{cwd: chatWT, projects: []apiclient.Project{home, api, web}, chats: []apiclient.Chat{chat}}, want: "web", why: whyCwd},
		{name: "worktree of an unlisted project is no candidate", in: startupInputs{cwd: wt, projects: []apiclient.Project{home, web}, tasks: []apiclient.Task{task}}, want: "home", why: whyCwd},
		{name: "cwd outside everything", in: startupInputs{cwd: dir("elsewhere"), defaultProject: "webapp", projects: all}, want: "webapp", why: whyConfig, silent: true},
		{name: "config beats last used", in: startupInputs{defaultProject: "api", lastUsed: &selectedProjectState{ID: 2, Name: "web"}, projects: all}, want: "api", why: whyConfig, silent: true},
		{
			name: "unknown config falls through to last used",
			in:   startupInputs{defaultProject: "gone", lastUsed: &selectedProjectState{ID: 2, Name: "web"}, projects: all},
			want: "web", why: whyLastUsed,
			noticeHas: []string{"tui.default_project `gone` is not registered — showing `web` (last used)"},
		},
		{name: "last used by id", in: startupInputs{lastUsed: &selectedProjectState{ID: 3, Name: "renamed"}, projects: all}, want: "webapp", why: whyLastUsed, silent: true},
		{name: "last used by name after renumbering", in: startupInputs{lastUsed: &selectedProjectState{ID: 77, Name: "web"}, projects: all}, want: "web", why: whyLastUsed, silent: true},
		{
			name: "stale last used falls through to first by name",
			in:   startupInputs{lastUsed: &selectedProjectState{ID: 77, Name: "old"}, projects: []apiclient.Project{web, api}},
			want: "api", why: whyFirstName,
			noticeHas: []string{"the last-used project `old` is not registered", "showing `api` (the first project by name)"},
		},
		{name: "first by name", in: startupInputs{projects: []apiclient.Project{web, webapp, api}}, want: "api", why: whyFirstName, silent: true},
		{
			name: "every failure is named",
			in:   startupInputs{flag: "x", defaultProject: "y", projects: all},
			want: "api", why: whyFirstName,
			noticeHas: []string{"--project `x`", "tui.default_project `y`"},
		},
		{name: "no projects", in: startupInputs{flag: "x", defaultProject: "y"}, noProjects: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveStartupProject(c.in)
			if c.noProjects {
				if got.ok || got.notice != "" {
					t.Fatalf("no projects: got %+v", got)
				}
				return
			}
			if !got.ok || got.project.Name != c.want || got.why != c.why {
				t.Fatalf("got %q by %q (ok=%v), want %q by %q", got.project.Name, got.why, got.ok, c.want, c.why)
			}
			if c.silent && got.notice != "" {
				t.Errorf("notice %q, want a silent pick", got.notice)
			}
			for _, s := range c.noticeHas {
				if !strings.Contains(got.notice, s) {
					t.Errorf("notice %q does not contain %q", got.notice, s)
				}
			}
		})
	}
}

// TestStartupCwdThroughSymlinkAndCase covers the two normalisations rule 2
// shares with SameDir: a symlinked spelling of the directory, and — on the
// platforms whose paths are case-insensitive — a case-only difference.
func TestStartupCwdThroughSymlinkAndCase(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "Repo")
	if err := os.MkdirAll(filepath.Join(target, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := apiclient.Project{ID: 1, Name: "repo", Path: target}
	other := apiclient.Project{ID: 2, Name: "aaa", Path: filepath.Join(root, "other")}

	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err == nil {
		got := resolveStartupProject(startupInputs{cwd: filepath.Join(link, "src"), projects: []apiclient.Project{other, p}})
		if got.project.ID != 1 || got.why != whyCwd {
			t.Errorf("symlinked cwd: got %+v", got)
		}
	} else {
		t.Logf("symlinks unavailable: %v", err)
	}

	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("case-sensitive paths here")
	}
	got := resolveStartupProject(startupInputs{cwd: filepath.Join(root, "repo", "SRC"), projects: []apiclient.Project{other, p}})
	if got.project.ID != 1 || got.why != whyCwd {
		t.Errorf("case-only cwd: got %+v", got)
	}
}

// TestStartupChainWaitsAndRunsOnce drives the root: nothing is selected until
// both the first project list and the first config answer are in, the pick
// is written to tui.json, and a later config answer naming another project
// never moves the selection (tui.default_project is read at startup only).
func TestStartupChainWaitsAndRunsOnce(t *testing.T) {
	// No client: the views hold no fetches to run, so every command the
	// root returns can be executed here. With no cwd the chain skips the
	// worktree listing.
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	run := func(msg tea.Msg) {
		t.Helper()
		_, cmd := m.Update(msg)
		for _, out := range flatten(cmd) {
			if w, ok := out.(startupWorktreesMsg); ok {
				_, next := m.Update(w)
				flatten(next)
			}
		}
	}
	m.projectsSeq = 1
	run(projectListMsg{seq: m.projectsSeq, projects: []apiclient.Project{testProject(1, "api"), testProject(2, "web")}})
	if m.sel.id != 0 {
		t.Fatalf("selected %+v before the config answered", m.sel)
	}
	run(boardConfigMsg{defaultProject: "ghost"})
	if m.sel.id != 1 || m.selWhy != whyFirstName {
		t.Fatalf("sel = %+v by %q, want api by first name", m.sel, m.selWhy)
	}
	if !strings.Contains(m.selNotice, "tui.default_project `ghost` is not registered") {
		t.Errorf("notice = %q", m.selNotice)
	}
	if line, ok := m.statusLine(); !ok || !strings.Contains(line, "ghost") {
		t.Errorf("status line = %q, %v", line, ok)
	}
	if st := readTUIState(m.dataDir).SelectedProject; st == nil || st.ID != 1 || st.Name != "api" {
		t.Errorf("the startup pick was not persisted: %+v", st)
	}

	// A hot reload naming web does not move the selection.
	run(daemonConfigMsg{config: apiclient.Config{TUI: apiclient.ConfigTUI{DefaultProject: "web"}}})
	run(boardConfigMsg{defaultProject: "web"})
	if m.sel.id != 1 {
		t.Fatalf("a later config answer moved the selection to %+v", m.sel)
	}
	// The notice is read by the next key.
	m.Update(keyPress("j"))
	if m.selNotice != "" {
		t.Errorf("notice survived a key: %q", m.selNotice)
	}
}

// TestSelectedProjectStateRoundTrips: selected_project joins tui.json
// without disturbing the fields it does not own.
func TestSelectedProjectStateRoundTrips(t *testing.T) {
	dir := ackedDir(t)
	raw := `{"full_auto_notice_ack": true, "chat_folds": [["web"]], "from_the_future": 3}`
	if err := os.WriteFile(statePath(dir), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeTUIState(dir, "selected_project", selectedProjectState{ID: 4, Name: "web"}); err != nil {
		t.Fatal(err)
	}
	st := readTUIState(dir)
	if st.SelectedProject == nil || *st.SelectedProject != (selectedProjectState{ID: 4, Name: "web"}) {
		t.Fatalf("selected_project = %+v", st.SelectedProject)
	}
	if !st.FullAutoNoticeAck || len(st.ChatFolds) != 1 {
		t.Errorf("known fields lost: %+v", st)
	}
	b, err := os.ReadFile(statePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["from_the_future"] != float64(3) {
		t.Errorf("unknown field lost: %s", b)
	}
}
