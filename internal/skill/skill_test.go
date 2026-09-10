package skill

import (
	"os"
	"path/filepath"
	"runtime"
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
			title: "store present with no readable manifest",
			seed:  func(t *testing.T, h string) { installAt(t, h, ".agents", name, "") },
			state: StateUnreadable,
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
		"--agent claude-code,codex,cursor --yes --global"
	if got := Command("vincent-workflows", nil); got != want {
		t.Errorf("Command() = %q, want %q", got, want)
	}
	if got := Command("vincent-workflows", Slugs([]string{"codex"})); !strings.Contains(got, "--agent codex ") {
		t.Errorf("a narrowed selection did not reach the argv: %q", got)
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
