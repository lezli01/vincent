package skill

import (
	"slices"
	"testing"
	"testing/fstest"
)

// codexLinks is the row's codex entries, store-read and ordinary.
func codexLinks(s Status) (store, linked []Link) {
	for _, l := range s.Links {
		if l.Agent != "codex" {
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

// TestDetectCodexReadsTheStore is issue #891. The skills CLI moved codex's
// global directory to the store itself (vercel-labs/skills#2067; skills
// 1.7.6's agent table has codex `globalSkillsDir: ~/.agents/skills`), so
// `skills add --agent codex -g` now leaves one store copy and no
// `~/.codex/skills/<name>` link. codex reads that copy itself: codex-cli
// 0.166.0's app-server `skills/list`, with a default CODEX_HOME, reports a
// store skill as `scope: user` from `~/.agents/skills` (and still does with
// CODEX_HOME relocated). A machine with `~/.codex` and the store copy is
// therefore one where codex is served, and the row must say so — exactly as
// copilot's store-read entry does (task 138.12).
func TestDetectCodexReadsTheStore(t *testing.T) {
	const name = "vincent-workflows"
	shipped := fstest.MapFS{name + "/SKILL.md": {Data: []byte(manifestFor(name, "1.2.0"))}}

	t.Run("codex and store present, no codex link", func(t *testing.T) {
		home := t.TempDir()
		mkdirs(t, home, ".codex")
		installAt(t, home, ".agents", name, "1.2.0")
		row := Detect(Options{Home: home, FS: shipped})[0]
		store, linked := codexLinks(row)
		if len(store) != 1 || len(linked) != 0 {
			t.Fatalf("codex entries: store-read %d, linked %d; links = %+v — a skill codex reads from ~/.agents/skills is reported as not reaching codex", len(store), len(linked), row.Links)
		}
		if store[0].Path != row.StorePath || store[0].Adapter != "codex" {
			t.Errorf("store-read link = %+v, want path %q", store[0], row.StorePath)
		}
		if !row.Linked() {
			t.Error("a skill codex reads is not linked")
		}
		if !slices.Contains(row.Agents(), "codex") {
			t.Errorf("agents = %v, want codex among them", row.Agents())
		}
		if row.State != StateCurrent {
			t.Errorf("state = %q (%s)", row.State, row.Message)
		}
	})

	t.Run("store present, codex absent", func(t *testing.T) {
		home := t.TempDir()
		installAt(t, home, ".agents", name, "1.2.0")
		row := Detect(Options{Home: home, FS: shipped})[0]
		if store, _ := codexLinks(row); len(store) != 0 {
			t.Errorf("reported codex reading a store on a machine without it: %+v", row.Links)
		}
	})
}
