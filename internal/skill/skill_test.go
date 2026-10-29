package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lezli01/vincent/skills"
)

// manifestFor is a minimal published SKILL.md with the given version. Written
// out rather than templated from the real one so a test never depends on the
// prose of the skill it is checking the marker of.
func manifestFor(name, version string) string {
	fm := "---\nname: " + name + "\ndescription: a test skill\n"
	if version != "" {
		fm += "metadata:\n  author: lezli01\n  version: " + version + "\n"
	}
	return fm + "---\n\n# body\n"
}

// TestPublishEnumeratesGenerically is decision 10: the published set is a
// glob over the embedded tree, so a second directory under `skills/` reaches
// every surface with no Go change. Proved against an injected fs.FS rather
// than by adding a second real skill.
func TestPublishEnumeratesGenerically(t *testing.T) {
	fsys := fstest.MapFS{
		"vincent-workflows/SKILL.md": {Data: []byte(manifestFor("vincent-workflows", "1.0.0"))},
		"vincent-reviews/SKILL.md":   {Data: []byte(manifestFor("vincent-reviews", "2.3.4"))},
		// Neither of these is a skill: one is a file at the root, the other a
		// reference under a skill. Both must be ignored by the glob.
		"README.md":                              {Data: []byte("nope")},
		"vincent-workflows/references/schema.md": {Data: []byte("nope")},
	}
	got := Publish(fsys)
	if len(got) != 2 {
		t.Fatalf("published %d skills, want 2: %+v", len(got), got)
	}
	// Sorted by embedded path, so the second directory is first.
	if got[0].Name != "vincent-reviews" || got[0].Version != "2.3.4" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Name != "vincent-workflows" || got[1].Version != "1.0.0" {
		t.Errorf("second = %+v", got[1])
	}
	if got[1].Description != "a test skill" {
		t.Errorf("description = %q", got[1].Description)
	}
}

// TestPublishRealSkill reads the copy this binary actually ships. It is the
// one assertion that the embedded front matter carries a version at all — the
// rest of the design is answerable only because it does.
func TestPublishRealSkill(t *testing.T) {
	got := Publish(skills.FS)
	if len(got) == 0 {
		t.Fatal("this build publishes no skills")
	}
	for _, p := range got {
		if p.Error != "" {
			t.Errorf("%s: %s", p.Path, p.Error)
		}
		if p.Version == "" {
			t.Errorf("%s: no metadata.version in the shipped front matter", p.Path)
		}
		if canonical(p.Version) == "" {
			t.Errorf("%s: metadata.version %q is not semver", p.Path, p.Version)
		}
		if p.Name == "" || p.Description == "" {
			t.Errorf("%s: name or description missing: %+v", p.Path, p)
		}
	}
}

func TestParseFrontMatter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		text    string
		version string
		wantErr bool
	}{
		{name: "version", text: manifestFor("s", "1.2.3"), version: "1.2.3"},
		{name: "no version", text: manifestFor("s", "")},
		{name: "crlf", text: strings.ReplaceAll(manifestFor("s", "9.9.9"), "\n", "\r\n"), version: "9.9.9"},
		{name: "no front matter", text: "# just a heading\n", wantErr: true},
		{name: "unterminated", text: "---\nname: s\n", wantErr: true},
		{name: "malformed yaml", text: "---\nname: [unclosed\n---\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseFrontMatter(tc.text)
			if tc.wantErr {
				if got.Error == "" {
					t.Fatalf("want an error, got %+v", got)
				}
				return
			}
			if got.Error != "" {
				t.Fatalf("unexpected error %q", got.Error)
			}
			if got.Version != tc.version {
				t.Errorf("version = %q, want %q", got.Version, tc.version)
			}
		})
	}
}

// installAt writes a skill copy under home/root/skills/name.
func installAt(t *testing.T, home, root, name, version string) string {
	t.Helper()
	dir := filepath.Join(home, root, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if version != "" {
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
			[]byte(manifestFor(name, version)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// linkInto is the CLI's default: a symlink from the agent's directory into
// the store. Windows makes creating one privileged, which is what `--copy`
// exists for, so the test skips rather than asserting a privilege.
func linkInto(t *testing.T, home, root, name, target string) {
	t.Helper()
	dir := filepath.Join(home, root, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("creating a symlink needs a privilege this runner may not have")
		}
		t.Fatal(err)
	}
}

// TestDetect is the detection table (§9.8): every state a row can report,
// over an injected home root.
func TestDetect(t *testing.T) {
	const name = "vincent-workflows"
	shipped := fstest.MapFS{name + "/SKILL.md": {Data: []byte(manifestFor(name, "1.2.0"))}}

	for _, tc := range []struct {
		title   string
		seed    func(t *testing.T, home string)
		state   string
		version string
		agents  []string
	}{
		{title: "store absent", seed: func(*testing.T, string) {}, state: StateAbsent},
		{
			title: "store current",
			seed:  func(t *testing.T, h string) { installAt(t, h, ".agents", name, "1.2.0") },
			state: StateCurrent, version: "1.2.0",
		},
		{
			title: "store older",
			seed:  func(t *testing.T, h string) { installAt(t, h, ".agents", name, "1.1.0") },
			state: StateOlder, version: "1.1.0",
		},
		{
			// A downgraded binary. The row must not call this up to date.
			title: "store newer",
			seed:  func(t *testing.T, h string) { installAt(t, h, ".agents", name, "2.0.0") },
			state: StateNewer, version: "2.0.0",
		},
		{
			title: "version not semver",
			seed:  func(t *testing.T, h string) { installAt(t, h, ".agents", name, "wednesday") },
			state: StateDiffers, version: "wednesday",
		},
		{
			title: "store present with no manifest at all",
			seed:  func(t *testing.T, h string) { installAt(t, h, ".agents", name, "") },
			state: StateUnreadable,
		},
		{
			// Every copy installed before the marker existed. A manifest that
			// reads perfectly and carries no version is not unreadable — it
			// predates versioning, which is a direction the row can claim.
			title: "store present with a manifest carrying no version",
			seed: func(t *testing.T, h string) {
				dir := installAt(t, h, ".agents", name, "")
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
					[]byte("---\nname: "+name+"\ndescription: old\n---\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			state: StateOlder,
		},
		{
			// A versioned copy anywhere beats an unversioned one: a version is
			// a better answer than the absence of one.
			title: "store unversioned, an agent holds a versioned copy",
			seed: func(t *testing.T, h string) {
				dir := installAt(t, h, ".agents", name, "")
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
					[]byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				installAt(t, h, ".claude", name, "1.2.0")
			},
			state: StateCurrent, version: "1.2.0", agents: []string{"claude"},
		},
		{
			title: "linked as a real copy",
			seed: func(t *testing.T, h string) {
				installAt(t, h, ".agents", name, "1.2.0")
				installAt(t, h, ".cursor", name, "1.2.0")
			},
			state: StateCurrent, version: "1.2.0", agents: []string{"cursor"},
		},
		{
			title: "linked by symlink",
			seed: func(t *testing.T, h string) {
				store := installAt(t, h, ".agents", name, "1.2.0")
				linkInto(t, h, ".claude", name, store)
			},
			state: StateCurrent, version: "1.2.0", agents: []string{"claude"},
		},
		{
			// One store serves every agent on the machine, so the row names
			// agents vincent does not drive rather than hiding them.
			title: "linked into an agent vincent does not drive",
			seed: func(t *testing.T, h string) {
				store := installAt(t, h, ".agents", name, "1.2.0")
				linkInto(t, h, ".claude", name, store)
				linkInto(t, h, ".zed", name, store)
			},
			state: StateCurrent, version: "1.2.0", agents: []string{"claude", "zed"},
		},
		{
			// No store, but an agent holds a real copy. Absent would be false.
			title: "no store, agent holds a copy",
			seed:  func(t *testing.T, h string) { installAt(t, h, ".codex", name, "1.0.0") },
			state: StateOlder, version: "1.0.0", agents: []string{"codex"},
		},
	} {
		t.Run(tc.title, func(t *testing.T) {
			home := t.TempDir()
			tc.seed(t, home)
			got := Detect(Options{Home: home, FS: shipped})
			if len(got) != 1 {
				t.Fatalf("got %d rows, want 1", len(got))
			}
			row := got[0]
			if row.State != tc.state {
				t.Errorf("state = %q (%s), want %q", row.State, row.Message, tc.state)
			}
			if row.Installed != tc.version {
				t.Errorf("installed = %q, want %q", row.Installed, tc.version)
			}
			if row.Shipped != "1.2.0" {
				t.Errorf("shipped = %q", row.Shipped)
			}
			if agents := strings.Join(row.Agents(), ","); agents != strings.Join(tc.agents, ",") {
				t.Errorf("agents = %q, want %q", agents, strings.Join(tc.agents, ","))
			}
			if row.State != StateAbsent && row.InstalledLabel() == "" {
				t.Error("an installed copy rendered as a nameless version")
			}
			if row.StorePath != filepath.Join(home, ".agents", "skills", name) {
				t.Errorf("store path = %q", row.StorePath)
			}
		})
	}
}

// TestDetectNamesTheAdapter proves the slug table is a table and not an
// identity: claude's directory is `.claude`, and the row says so, while its
// `--agent` slug is `claude-code`.
func TestDetectNamesTheAdapter(t *testing.T) {
	const name = "s"
	home := t.TempDir()
	installAt(t, home, ".claude", name, "1.0.0")
	installAt(t, home, ".zed", name, "1.0.0")
	got := Detect(Options{
		Home: home,
		FS:   fstest.MapFS{name + "/SKILL.md": {Data: []byte(manifestFor(name, "1.0.0"))}},
	})
	links := got[0].Links
	if len(links) != 2 {
		t.Fatalf("links = %+v", links)
	}
	if links[0].Agent != "claude" || links[0].Adapter != "claude" {
		t.Errorf("claude link = %+v", links[0])
	}
	if links[1].Agent != "zed" || links[1].Adapter != "" {
		t.Errorf("zed link = %+v; an agent vincent does not drive carries no adapter", links[1])
	}
}

// TestSlugs pins the adapter → `skills --agent` mapping, including that
// claude's slug is not `claude`.
func TestSlugs(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{in: nil, want: "claude-code,codex,cursor"},
		{in: []string{}, want: "claude-code,codex,cursor"},
		{in: []string{"claude"}, want: "claude-code"},
		{in: []string{"cursor", "claude"}, want: "claude-code,cursor"},
		{in: []string{"emacs"}, want: "claude-code,codex,cursor"},
	} {
		if got := strings.Join(Slugs(tc.in), ","); got != tc.want {
			t.Errorf("Slugs(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestInstallArgs is the assertion that catches the published command and the
// command vincent runs drifting apart (decision 1). README.md and
// docs/guides/workflows.md print the interactive spelling of this line.
func TestInstallArgs(t *testing.T) {
	want := "npx skills add lezli01/vincent --skill vincent-workflows " +
		"--agent claude-code codex cursor --yes --global"
	if got := Command("vincent-workflows", nil); got != want {
		t.Errorf("Command() = %q, want %q", got, want)
	}
	if got := Command("vincent-workflows", Slugs([]string{"codex"})); !strings.Contains(got, "--agent codex ") {
		t.Errorf("a narrowed selection did not reach the argv: %q", got)
	}
	got := InstallArgs("vincent-workflows", nil)
	for _, a := range got {
		if strings.Contains(a, ",") {
			t.Errorf("InstallArgs joined slugs into one element %q; skills --agent is variadic", a)
		}
	}
}

func TestMissing(t *testing.T) {
	got := Missing([]Status{
		{Name: "b", State: StateAbsent},
		{Name: "a", State: StateCurrent},
		{Name: "c", State: StateOlder},
	})
	if strings.Join(got, ",") != "b,c" {
		t.Errorf("Missing = %v", got)
	}
}

// copilotLinks is the row's copilot entries, store-read and ordinary.
func copilotLinks(s Status) (store, linked []Link) {
	for _, l := range s.Links {
		if l.Agent != "copilot" {
			continue
		}
		if l.Store {
			store = append(store, l)
		} else {
			linked = append(linked, l)
		}
	}
	return store, linked
}

// mkdirs creates home/root for each root — an agent being on this machine.
func mkdirs(t *testing.T, home string, roots ...string) {
	t.Helper()
	for _, r := range roots {
		if err := os.MkdirAll(filepath.Join(home, r), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDetectCopilotReadsTheStore is task 138.12 decision 2: copilot loads
// `~/.agents/skills` itself, so it is reported as serving the skill when its
// root exists and the store copy does — and only then.
func TestDetectCopilotReadsTheStore(t *testing.T) {
	const name = "vincent-workflows"
	shipped := fstest.MapFS{name + "/SKILL.md": {Data: []byte(manifestFor(name, "1.2.0"))}}

	t.Run("copilot and store present", func(t *testing.T) {
		home := t.TempDir()
		mkdirs(t, home, ".copilot")
		installAt(t, home, ".agents", name, "1.2.0")
		row := Detect(Options{Home: home, FS: shipped})[0]
		store, linked := copilotLinks(row)
		if len(store) != 1 || len(linked) != 0 {
			t.Fatalf("links = %+v", row.Links)
		}
		if store[0].Path != row.StorePath || store[0].Adapter != "copilot" {
			t.Errorf("store-read link = %+v, want path %q", store[0], row.StorePath)
		}
		if !row.Linked() {
			t.Error("a skill copilot reads is not linked")
		}
		if got := strings.Join(row.AgentLabels(), ","); got != "copilot (reads store)" {
			t.Errorf("labels = %q", got)
		}
		if row.State != StateCurrent {
			t.Errorf("state = %q (%s)", row.State, row.Message)
		}
	})
	for _, tc := range []struct {
		title string
		seed  func(t *testing.T, home string)
	}{
		{title: "copilot present, store absent", seed: func(t *testing.T, h string) { mkdirs(t, h, ".copilot") }},
		{title: "copilot present, only a copy in claude", seed: func(t *testing.T, h string) {
			mkdirs(t, h, ".copilot")
			installAt(t, h, ".claude", name, "1.2.0")
		}},
		{title: "store present, copilot absent", seed: func(t *testing.T, h string) {
			installAt(t, h, ".agents", name, "1.2.0")
		}},
	} {
		t.Run(tc.title, func(t *testing.T) {
			home := t.TempDir()
			tc.seed(t, home)
			row := Detect(Options{Home: home, FS: shipped})[0]
			if store, _ := copilotLinks(row); len(store) != 0 {
				t.Errorf("reported copilot reading a store it cannot: %+v", row.Links)
			}
		})
	}
	t.Run("copilot holds a link as well", func(t *testing.T) {
		const other = "vincent-triggers"
		home := t.TempDir()
		store := installAt(t, home, ".agents", other, "1.2.0")
		linkInto(t, home, ".copilot", other, store)
		row := Detect(Options{
			Home: home,
			FS:   fstest.MapFS{other + "/SKILL.md": {Data: []byte(manifestFor(other, "1.2.0"))}},
		})[0]
		storeRead, linked := copilotLinks(row)
		if len(storeRead) != 1 || len(linked) != 1 {
			t.Fatalf("want one ordinary link and one store-read entry: %+v", row.Links)
		}
		if linked[0].Adapter != "copilot" || linked[0].Store {
			t.Errorf("ordinary link = %+v", linked[0])
		}
		if got := strings.Join(row.AgentLabels(), ","); got != "copilot,copilot (reads store)" {
			t.Errorf("labels = %q", got)
		}
	})
}

// TestStoreResidentSlugs: copilot is an adapter vincent installs for, and it
// adds nothing to the argv.
func TestStoreResidentSlugs(t *testing.T) {
	if !slices.Contains(Adapters(), "copilot") {
		t.Errorf("Adapters() = %v, want copilot listed", Adapters())
	}
	if !StoreResident("copilot") || StoreResident("claude") {
		t.Error("StoreResident does not mark copilot alone")
	}
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{in: nil, want: "claude-code,codex,cursor"},
		{in: []string{"copilot"}, want: ""},
		{in: []string{"copilot", "claude"}, want: "claude-code"},
		{in: []string{"bogus"}, want: "claude-code,codex,cursor"},
	} {
		if got := strings.Join(Slugs(tc.in), ","); got != tc.want {
			t.Errorf("Slugs(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPlanInstall is decision 4's rule, the one the CLI and TUI share.
func TestPlanInstall(t *testing.T) {
	const name = "vincent-workflows"
	shipped := fstest.MapFS{name + "/SKILL.md": {Data: []byte(manifestFor(name, "1.2.0"))}}
	home := t.TempDir()
	absent := Detect(Options{Home: home, FS: shipped})

	if p := PlanInstall(name, []string{"copilot", "claude"}, absent); !p.Run() ||
		strings.Join(p.Slugs, ",") != "claude-code" {
		t.Errorf("mixed selection = %+v", p)
	}
	if p := PlanInstall(name, nil, absent); strings.Join(p.Slugs, ",") != "claude-code,codex,cursor" {
		t.Errorf("default selection = %+v", p)
	}
	p := PlanInstall(name, []string{"copilot"}, absent)
	if p.Run() || p.Err == nil {
		t.Fatalf("copilot alone with no store copy = %+v, want an error", p)
	}
	if msg := p.Err.Error(); !strings.Contains(msg, "--agent claude") || !strings.Contains(msg, "none is installed") {
		t.Errorf("error does not say how to populate the store: %q", msg)
	}

	installAt(t, home, ".agents", name, "1.2.0")
	current := Detect(Options{Home: home, FS: shipped})
	p = PlanInstall(name, []string{"copilot"}, current)
	if p.Run() || p.Err != nil || !strings.Contains(p.Note, "nothing to link") {
		t.Errorf("copilot alone with a current store copy = %+v", p)
	}

	// Review F2: a store copy that is there but not current is what Missing
	// offers to fix; copilot reading it is not the skill installed.
	for version, state := range map[string]string{"1.0.0": StateOlder, "next": StateDiffers} {
		installAt(t, home, ".agents", name, version)
		stale := Detect(Options{Home: home, FS: shipped})
		if got := stale[0].State; got != state {
			t.Fatalf("store at %s: state %s, want %s", version, got, state)
		}
		p = PlanInstall(name, []string{"copilot"}, stale)
		if p.Run() || p.Err == nil || p.Note != "" {
			t.Fatalf("copilot alone with a store copy at %s = %+v, want an error", version, p)
		}
		if msg := p.Err.Error(); !strings.Contains(msg, "which is "+state) || !strings.Contains(msg, "--agent claude") {
			t.Errorf("error does not say the store is %s and how to refresh it: %q", state, msg)
		}
	}
}

// TestStoreLinkJSON: the store-read entry reaches the wire as `store: true`.
func TestStoreLinkJSON(t *testing.T) {
	b, err := json.Marshal(Link{Agent: "copilot", Adapter: "copilot", Path: "p", Store: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"store":true`) {
		t.Errorf("json = %s", b)
	}
	b, _ = json.Marshal(Link{Agent: "claude"})
	if strings.Contains(string(b), "store") {
		t.Errorf("an ordinary link carries store: %s", b)
	}
}
