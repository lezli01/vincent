package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The Settings screen (task 152). Pure model tests, one property each.

func settingsFixture(t *testing.T) *settingsView {
	t.Helper()
	v := newSettingsView()
	v.update(tea.WindowSizeMsg{Width: 180, Height: 44})
	v.update(daemonConfigMsg{config: defaultClientConfig()})
	return v
}

func mustSetting(t *testing.T, v *settingsView, path string) configKey {
	t.Helper()
	k, ok := v.key(path)
	if !ok {
		t.Fatalf("no key %q", path)
	}
	return k
}

// Every key the daemon can be told is on the screen, once.
func TestSettingsCatalogCoversEveryKey(t *testing.T) {
	seen := map[string]int{}
	for _, c := range settingsCatalog {
		for _, s := range c.sections {
			for _, item := range s.items {
				seen[item.path]++
				if item.label == "" || item.desc == "" {
					t.Errorf("%s has no label or description", item.path)
				}
			}
		}
	}
	for _, k := range configKeys() {
		if seen[k.path] != 1 {
			t.Errorf("%s is listed %d times", k.path, seen[k.path])
		}
		delete(seen, k.path)
	}
	for path := range seen {
		t.Errorf("the catalogue lists %q, which is no key", path)
	}
}

// Every width draws exactly the screen it was given.
func TestSettingsFitsTheScreen(t *testing.T) {
	for _, size := range [][2]int{{180, 44}, {130, 24}, {240, 60}} {
		v := settingsFixture(t)
		v.update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		lines := strings.Split(v.render(size[0], size[1]), "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: drew %d lines", size[0], size[1], len(lines))
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != size[0] {
				t.Errorf("%dx%d: line %d is %d wide", size[0], size[1], i, got)
			}
		}
	}
}

func TestSettingsShowsTheThreePanes(t *testing.T) {
	out := ansi.Strip(settingsFixture(t).render(180, 44))
	for _, want := range []string{
		"Settings", "Work & limits", "Daemon", "Tasks at once", "Chats at once",
		"About this setting", "max_parallel_tasks", "search every setting",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen is missing %q:\n%s", want, out)
		}
	}
}

// Every category stays on screen on the shortest terminal that gets the panes.
func TestSettingsListsEveryCategoryWhenShort(t *testing.T) {
	v := settingsFixture(t)
	v.update(tea.WindowSizeMsg{Width: 180, Height: formWideMinH})
	out := ansi.Strip(v.render(180, formWideMinH))
	for _, c := range settingsCatalog {
		if !strings.Contains(out, c.name) {
			t.Errorf("the list lost %q:\n%s", c.name, out)
		}
	}
}

// The confirmation keeps the section it belongs to in view.
func TestSettingsConfirmKeepsItsSectionInView(t *testing.T) {
	v := settingsFixture(t)
	v.update(tea.WindowSizeMsg{Width: 180, Height: 34})
	v.openKey("agents.claude.path")
	v.form.input.SetValue("/opt/claude")
	v.updateKey(namedKey("enter"))
	out := ansi.Strip(v.render(180, 34))
	for _, want := range []string{"Agent programs", "Change what vincent runs"} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen is missing %q:\n%s", want, out)
		}
	}
}

// A switch's default reads the way its control does.
func TestSettingsDefaultsReadLikeTheirControls(t *testing.T) {
	v := settingsFixture(t)
	if got := settingsDefault(mustSetting(t, v, "fetch_base_branch")); got != "on" {
		t.Errorf("the default reads %q", got)
	}
}

// Opening the template editor asks for a preview without waiting for a key.
func TestSettingsPreviewsWhenTheTemplateOpens(t *testing.T) {
	v := settingsFixture(t)
	v.client = offlineClient()
	v.config.BranchTemplate = "feat/{{.ID}}"
	v.category, v.selected = 2, "branch_template"
	if v.updateKey(namedKey("enter")) == nil {
		t.Fatal("opening the editor asked for no preview")
	}
}

// A small terminal keeps every setting and every key.
func TestSettingsNarrowKeepsTheSettings(t *testing.T) {
	v := settingsFixture(t)
	v.update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if v.frameless() {
		t.Fatal("a narrow terminal asked for the panes")
	}
	if out := ansi.Strip(v.render(78, 22)); !strings.Contains(out, "Tasks at once") {
		t.Errorf("the settings are gone:\n%s", out)
	}
}

// An arrow changes a value at once, and holding it sends the value it ends on.
func TestSettingsArrowsChangeAndCoalesce(t *testing.T) {
	v := settingsFixture(t)
	v.client = offlineClient()
	first := v.updateKey(namedKey("right"))
	if first == nil {
		t.Fatal("the first press sent nothing")
	}
	for range 2 {
		if v.updateKey(namedKey("right")) != nil {
			t.Fatal("a press behind one in flight sent its own request")
		}
	}
	if got := v.shown["max_parallel_tasks"]; got != "6" {
		t.Fatalf("the screen shows %q, want 6", got)
	}
	cfg := defaultClientConfig()
	cfg.MaxParallelTasks = 4
	if v.applySaved(configSavedMsg{path: "max_parallel_tasks", cfg: cfg}) == nil {
		t.Fatal("the value the arrow ended on was never sent")
	}
}

// A refusal puts the value back and says why.
func TestSettingsRefusalRestoresTheValue(t *testing.T) {
	v := settingsFixture(t)
	v.client = offlineClient()
	v.updateKey(namedKey("right"))
	v.applySaved(configSavedMsg{path: "max_parallel_tasks", err: errDaemonUnreachable})
	k := mustSetting(t, v, "max_parallel_tasks")
	if v.value(k) != "3" || !v.status().bad {
		t.Fatalf("value %q, note %q", v.value(k), v.status().text)
	}
}

// Durations walk their presets, whatever spelling the daemon serves.
func TestSettingsDurationsWalkThePresets(t *testing.T) {
	item := settingsItem{presets: []string{"0s", "5m", "15m", "1h"}}
	k := configKey{kind: kindText}
	if got, _ := nextValue(item, k, "5m0s", 1); got != "15m" {
		t.Errorf("right from 5m0s went to %q", got)
	}
	if got, _ := nextValue(item, k, "7m", 1); got != "0s" {
		t.Errorf("right from a custom value went to %q", got)
	}
}

// No path changes a dangerous key without the confirmation.
func TestSettingsDangerousKeysAlwaysAsk(t *testing.T) {
	v := settingsFixture(t)
	v.client = offlineClient()
	v.openKey("triggers.enabled")
	v.form = nil
	if cmd := v.updateKey(namedKey("right")); cmd != nil {
		t.Fatal("the arrow sent a dangerous key")
	}
	if v.form == nil || !v.form.confirming {
		t.Fatal("the arrow did not stop at the confirmation")
	}
	if out := ansi.Strip(v.render(180, 44)); !strings.Contains(out, "without you asking") {
		t.Errorf("the card does not say what is at stake:\n%s", out)
	}
	if keys := v.promptKeys(); len(keys) == 0 || keys[0] != "y" {
		t.Errorf("the status bar offers %v while the confirmation waits", keys)
	}

	v = settingsFixture(t)
	v.client = offlineClient()
	v.config.Agents.Claude.Path = "/opt/claude"
	v.openKey("agents.claude.path")
	v.form = nil
	if cmd := v.updateKey(namedKey("backspace")); cmd != nil {
		t.Fatal("reset sent a dangerous key")
	}
	if v.form == nil || !v.form.confirming {
		t.Fatal("reset did not stop at the confirmation")
	}
}

// Search finds settings in every category, and clears back to where you were.
func TestSettingsSearchSpansCategories(t *testing.T) {
	v := settingsFixture(t)
	v.updateKey(namedKey("/"))
	for _, r := range "poll" {
		v.updateKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	paths := []string{}
	for _, r := range v.rows() {
		paths = append(paths, r.item.path)
	}
	for _, want := range []string{"github.poll_interval", "jira.poll_interval", "update.poll_interval"} {
		if !slices.Contains(paths, want) {
			t.Errorf("the search missed %s: %v", want, paths)
		}
	}
	v.updateKey(namedKey("esc"))
	if v.searching || v.search.Value() != "" || len(v.rows()) != 10 {
		t.Errorf("esc left searching=%v value=%q rows=%d", v.searching, v.search.Value(), len(v.rows()))
	}
}

// Tab walks the categories and lands on the first setting of each.
func TestSettingsTabMovesCategory(t *testing.T) {
	v := settingsFixture(t)
	v.updateKey(namedKey("tab"))
	if v.category != 1 || v.selected != "defaults.agent_timeout" {
		t.Fatalf("category %d, selected %q", v.category, v.selected)
	}
	v.updateKey(namedKey("shift+tab"))
	v.updateKey(namedKey("shift+tab"))
	if v.category != len(settingsCatalog)-1 {
		t.Fatalf("shift+tab did not wrap: %d", v.category)
	}
}

// A setting that differs from its default says so, on the row and in the list.
func TestSettingsMarksWhatDiffersFromTheDefault(t *testing.T) {
	v := settingsFixture(t)
	v.config.MaxParallelTasks = 8
	if v.changedIn(0) != 1 {
		t.Fatalf("changedIn = %d", v.changedIn(0))
	}
	if out := ansi.Strip(v.render(180, 44)); !strings.Contains(out, "default 3") {
		t.Errorf("the row does not name its default:\n%s", out)
	}
}

// The template editor shows what the daemon says the name would be.
func TestSettingsPreviewsTheBranchTemplate(t *testing.T) {
	v := settingsFixture(t)
	v.openKey("branch_template")
	v.form.input.SetValue("feat/{{.ID}}")
	v.applyPreview(branchPreviewMsg{template: "feat/{{.ID}}", preview: apiclient.BranchPreview{
		Branch: "feat/128", TaskID: 128, Title: "a task",
	}})
	if out := ansi.Strip(v.render(180, 44)); !strings.Contains(out, "feat/128") {
		t.Errorf("the preview is missing:\n%s", out)
	}
	v.applyPreview(branchPreviewMsg{template: "feat/{{.ID}}", preview: apiclient.BranchPreview{Error: "bad template"}})
	if out := ansi.Strip(v.render(180, 44)); !strings.Contains(out, "bad template") {
		t.Errorf("the template's error is missing:\n%s", out)
	}
	v.updateKey(namedKey("tab"))
	if !strings.Contains(v.form.input.Value(), "{{.ID}}{{.ID}}") {
		t.Errorf("tab did not insert the field: %q", v.form.input.Value())
	}
}

// Another screen can send somebody straight to one setting.
func TestSettingsOpensAKeyOnceTheConfigLands(t *testing.T) {
	v := newSettingsView()
	v.openKey("triggers.enabled")
	if v.form != nil {
		t.Fatal("the editor opened with no config to seed it")
	}
	v.update(daemonConfigMsg{config: defaultClientConfig()})
	if v.form == nil || v.form.key.path != "triggers.enabled" {
		t.Fatalf("the editor is on %+v", v.form)
	}
}

// #1057: after a change the screen says what the value was, and u puts it
// back with one write of its own, which is not itself undoable.
func TestSettingsSaysWhatItWasAndUndoes(t *testing.T) {
	v := settingsFixture(t)
	v.client = offlineClient()
	v.selected = "max_parallel_tasks"
	k := mustSetting(t, v, v.selected)
	before := k.read(v.config)
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyRight})
	changed := v.config
	changed.MaxParallelTasks++
	v.update(configSavedMsg{path: k.path, cfg: changed})
	if v.undo == nil || v.undo.was != before {
		t.Fatalf("undo = %+v, want the value it replaced (%q)", v.undo, before)
	}
	if !strings.Contains(v.status().text, "was "+before) || !strings.Contains(v.status().text, "u puts it back") {
		t.Fatalf("note = %q", v.status().text)
	}
	v.updateKey(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if got := v.value(k); got != before {
		t.Fatalf("u sent %q, want %q", got, before)
	}
	restored := changed
	restored.MaxParallelTasks--
	v.update(configSavedMsg{path: k.path, cfg: restored})
	if v.undo != nil || !strings.Contains(v.status().text, "put back") {
		t.Fatalf("after the undo: undo %+v, note %q", v.undo, v.status().text)
	}
	v.updateKey(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if v.status().text != "nothing to undo" {
		t.Fatalf("a second u said %q", v.status().text)
	}
}

// #1137: an undo that never landed — an esc at a dangerous key's
// confirmation, or a write the daemon refused — leaves nothing behind, so
// the next ordinary change is offered as undoable, not reported "put back".
func TestSettingsUndoClearsOnEscAndOnFailure(t *testing.T) {
	saveNext := func(t *testing.T, v *settingsView) {
		t.Helper()
		v.form = nil
		v.goTo("max_parallel_tasks")
		k := mustSetting(t, v, v.selected)
		v.updateKey(tea.KeyPressMsg{Code: tea.KeyRight})
		changed := v.config
		changed.MaxParallelTasks++
		v.update(configSavedMsg{path: k.path, cfg: changed})
		if strings.Contains(v.status().text, "put back") || !strings.Contains(v.status().text, "u puts it back") || v.undo == nil || v.undo.key.path != k.path {
			t.Fatalf("the next change: note %q, undo %+v", v.status().text, v.undo)
		}
	}

	v := settingsFixture(t)
	v.client = offlineClient()
	v.undo = &settingsUndo{key: mustSetting(t, v, "triggers.enabled"), was: "true"}
	v.updateKey(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if v.form == nil || !v.form.confirming {
		t.Fatal("u on a dangerous key did not stop at the confirmation")
	}
	v.updateKey(namedKey("esc"))
	if v.undoing {
		t.Fatal("esc at the confirmation left the undo pending")
	}
	saveNext(t, v)

	v = settingsFixture(t)
	v.client = offlineClient()
	k := mustSetting(t, v, "max_parallel_tasks")
	v.undo = &settingsUndo{key: k, was: "1"}
	v.updateKey(tea.KeyPressMsg{Code: 'u', Text: "u"})
	v.update(configSavedMsg{path: k.path, err: errors.New("refused")})
	if v.undoing {
		t.Fatal("a refused undo left the undo pending")
	}
	saveNext(t, v)
}

// #1137: u pressed in another category goes to the setting it puts back and
// names it, rather than rewriting a value off screen.
func TestSettingsUndoNamesTheSettingAndShowsIt(t *testing.T) {
	v := settingsFixture(t)
	v.client = offlineClient()
	v.selected = "max_parallel_tasks"
	k := mustSetting(t, v, v.selected)
	home := v.category
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyRight})
	changed := v.config
	changed.MaxParallelTasks++
	v.update(configSavedMsg{path: k.path, cfg: changed})
	v.updateKey(namedKey("tab"))
	if v.category == home {
		t.Fatal("tab did not leave the category")
	}
	v.updateKey(tea.KeyPressMsg{Code: 'u', Text: "u"})
	if v.category != home || v.selected != k.path {
		t.Fatalf("u left the screen on category %d, %q", v.category, v.selected)
	}
	if row, ok := v.current(); !ok || row.item.path != k.path {
		t.Fatal("the setting u puts back is not on screen")
	}
	label := ""
	for _, r := range v.rows() {
		if r.item.path == k.path {
			label = r.item.label
		}
	}
	restored := changed
	restored.MaxParallelTasks--
	v.update(configSavedMsg{path: k.path, cfg: restored})
	if !strings.Contains(v.status().text, label+" put back") {
		t.Fatalf("the undo's note %q does not name %q", v.status().text, label)
	}
}
