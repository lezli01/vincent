package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/keymap"
)

// guideKeyContexts are the board and task-workspace surfaces whose keys
// docs/guides/tui.md tabulates (issue #592). Task actions are held too: the
// workspace's action bar is one of the guide's tables.
var guideKeyContexts = map[bindingContext]bool{
	ctxTasks:           true,
	ctxTaskOverview:    true,
	ctxTimeline:        true,
	ctxTaskDetails:     true,
	ctxOutput:          true,
	ctxDiff:            true,
	ctxTaskWorkflow:    true,
	ctxTaskStepDetails: true,
	ctxTaskPull:        true,
}

// guideFixedSurfaces are the keymap.FixedKeys surfaces whose keys the same
// tables may name: an unregistered alias or a multi-key set is a real key even
// though no registry row carries it.
var guideFixedSurfaces = map[keymap.Surface]bool{
	"task table":        true,
	"task overview":     true,
	"timeline":          true,
	"task details":      true,
	"output":            true,
	"diff":              true,
	"task workflow":     true,
	"task step details": true,
	"task pull request": true,
	"task workspace":    true,
	"lists and panes":   true,
	"global":            true,
}

// TestGuideKeyTablesMatchRegistry holds the TUI guide's board and workspace
// key tables to the binding registry in both directions (issue #592). The
// redesign under #589 renames tabs and moves keys; without this, the guide
// keeps promising a key after the registry has moved it, which is how it came
// to say "five tabs … 1–5" while the workspace had seven.
//
// It matches on keys, not labels: the guide's "Does" column is prose written
// for readers and deliberately not the registry's label.
func TestGuideKeyTablesMatchRegistry(t *testing.T) {
	guide := guideKeyTables(t)

	var rows []binding
	for _, b := range registry() {
		if b.key == "" {
			continue
		}
		if b.scope == scopeTaskAction || b.scope == scopePanel && guideKeyContexts[b.context] {
			rows = append(rows, b)
		}
	}
	if len(rows) == 0 {
		t.Fatal("no registry row in the board or workspace contexts; the scope is stale")
	}

	t.Run("every registry row is in the guide", func(t *testing.T) {
		for _, b := range rows {
			for _, k := range splitRegistryKey(b.key) {
				if _, ok := guide[k]; !ok {
					t.Errorf("%s: key %q (%q) is in no key table of docs/guides/tui.md's board or task sections",
						contextName(b), k, b.label)
				}
			}
		}
	})

	t.Run("every guide key is in the registry", func(t *testing.T) {
		known := map[string]bool{}
		for _, b := range rows {
			for _, k := range splitRegistryKey(b.key) {
				known[k] = true
			}
			for _, k := range labelAliases(b.label) {
				known[k] = true
			}
		}
		for _, f := range keymap.FixedKeys() {
			if guideFixedSurfaces[f.Surface] {
				known[normalizeKey(f.Key)] = true
			}
		}
		keys := make([]string, 0, len(guide))
		for k := range guide {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !known[k] {
				t.Errorf("docs/guides/tui.md:%d names key %q, which no board or workspace registry row, "+
					"row label or fixed key carries", guide[k], k)
			}
		}
	})
}

func contextName(b binding) string {
	if b.scope == scopeTaskAction {
		return "task action"
	}
	return string(b.context)
}

// guideKeyTables reads the key tables of the guide's board and task sections —
// from "## The board" up to "## Repairing a blocked task" — and of the
// Workflow tab's subsection, which lives under the takeover screens. It
// returns each normalized key with the first guide line naming it. Only tables
// whose first header cell is "Key" count, which leaves out the clipboard's Row
// table and the verbosity Level table.
func guideKeyTables(t *testing.T) map[string]int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "guides", "tui.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")

	keys := map[string]int{}
	// The board span runs across headings of every level; the Workflow tab's
	// subsection ends at the next heading of level four or less.
	inScope, inWorkflowTab, inTable := false, false, false
	sawBoard, sawWorkflowTab := false, false
	for i, line := range lines {
		if level := headingLevel(line); level > 0 {
			switch {
			case line == "## The board":
				inScope, sawBoard = true, true
			case line == "## Repairing a blocked task":
				inScope = false
			case strings.HasPrefix(line, "#### The same picture for a running task"):
				inScope, inWorkflowTab, sawWorkflowTab = true, true, true
			case inWorkflowTab && level <= 4:
				inScope, inWorkflowTab = false, false
			}
			inTable = false
			continue
		}
		if !strings.HasPrefix(line, "|") {
			inTable = false
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		first := strings.TrimSpace(cells[1])
		if !inTable {
			inTable = inScope && first == "Key"
			continue
		}
		if strings.Trim(first, "-: ") == "" {
			continue
		}
		for _, k := range guideCellKeys(first) {
			if _, ok := keys[k]; !ok {
				keys[k] = i + 1
			}
		}
	}
	if !sawBoard || !sawWorkflowTab {
		t.Fatalf("docs/guides/tui.md lost a heading this test scopes by (board %v, Workflow tab %v)",
			sawBoard, sawWorkflowTab)
	}
	if len(keys) == 0 {
		t.Fatal("no key table found in docs/guides/tui.md's board and task sections")
	}
	return keys
}

func headingLevel(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n >= len(line) || line[n] != ' ' {
		return 0
	}
	return n
}

var (
	backticked = regexp.MustCompile("`([^`]+)`")
	digitRange = regexp.MustCompile("`(\\d)`\\s*[–-]\\s*`(\\d)`")
)

// guideCellKeys is the keys a table row's first cell names: every backticked
// span, with a `1`–`5` range expanded, `hjkl` split into its four keys, and
// "`shift` + arrows" read as the four shift+arrow presses.
func guideCellKeys(cell string) []string {
	var out []string
	if m := digitRange.FindStringSubmatch(cell); m != nil {
		for d := m[1][0]; d <= m[2][0]; d++ {
			out = append(out, string(d))
		}
		cell = digitRange.ReplaceAllString(cell, "")
	}
	if strings.Contains(cell, "`shift` + arrows") {
		for _, a := range []string{"up", "down", "left", "right"} {
			out = append(out, "shift+"+a)
		}
		cell = strings.ReplaceAll(cell, "`shift` + arrows", "")
	}
	for _, m := range backticked.FindAllStringSubmatch(cell, -1) {
		if m[1] == "hjkl" {
			out = append(out, "h", "j", "k", "l")
			continue
		}
		out = append(out, normalizeKey(m[1]))
	}
	return out
}

// normalizeKey spells a key the way the registry does: the guide's arrow
// glyphs are the registry's names, and pgdn is pgdown, behind any modifier.
func normalizeKey(k string) string {
	if i := strings.LastIndex(k, "+"); i > 0 && i < len(k)-1 {
		return k[:i+1] + normalizeKey(k[i+1:])
	}
	switch k {
	case "↑":
		return "up"
	case "↓":
		return "down"
	case "←":
		return "left"
	case "→":
		return "right"
	case "pgdn":
		return "pgdown"
	}
	return k
}

// splitRegistryKey reads a registry key naming more than one press, such as
// an O/C pair, as its parts.
func splitRegistryKey(k string) []string {
	if k == "/" || !strings.Contains(k, "/") {
		return []string{normalizeKey(k)}
	}
	var out []string
	for _, p := range strings.Split(k, "/") {
		if p != "" {
			out = append(out, normalizeKey(p))
		}
	}
	return out
}

var namedKeys = map[string]bool{
	"tab": true, "enter": true, "space": true, "esc": true, "pgup": true, "pgdn": true, "pgdown": true,
}

// labelAliases is the keys a registry label names as aliases of its own:
// "shift+tab goes back", "[ goes back", "1–4 jump directly", "f/G",
// "←/→ or h/l", "or hjkl", "enter and →/← too", "ctrl+s posts".
func labelAliases(label string) []string {
	fields := strings.Fields(label)
	var out []string
	for i, f := range fields {
		f = strings.Trim(f, "(),;:")
		if f == "" {
			continue
		}
		switch {
		case f == "hjkl":
			out = append(out, "h", "j", "k", "l")
		case len([]rune(f)) == 3 && strings.ContainsRune(f, '–'):
			r := []rune(f)
			for d := r[0]; d <= r[2]; d++ {
				out = append(out, string(d))
			}
		case strings.Contains(f, "/") && f != "/":
			// "shift+↑/↓/←/→": the first press's modifier holds for the rest.
			mod := ""
			for _, p := range strings.Split(f, "/") {
				if p == "" {
					continue
				}
				if i := strings.LastIndex(p, "+"); i > 0 {
					mod = p[:i+1]
				} else {
					p = mod + p
				}
				out = append(out, normalizeKey(p))
			}
		case strings.Contains(f, "+") || namedKeys[f]:
			out = append(out, normalizeKey(f))
		case i+1 < len(fields) && fields[i+1] == "goes":
			out = append(out, normalizeKey(f))
		}
	}
	return out
}

var (
	pngReference = regexp.MustCompile(`tui-[a-z0-9-]+\.png`)
	shotTarget   = regexp.MustCompile(`Screenshot "[^\n]*?(tui-[a-z0-9-]+\.png)"`)
	tapeCall     = regexp.MustCompile(`(?m)^\s*tape (tui-[a-z0-9-]+) `)
)

// TestScreenshotReferencesMatchTapes holds the docs' TUI screenshots to
// scripts/screenshots.sh, the only thing allowed to produce them (issue
// #592): every referenced PNG exists and some tape's Screenshot writes it,
// every Screenshot is referenced somewhere, and every tape writes the PNG
// named after it, which is the file tape() itself fails without. A tape may
// write more than one (tui-chat-skills also takes tui-chat-skills-inline).
func TestScreenshotReferencesMatchTapes(t *testing.T) {
	root := filepath.Join("..", "..")

	referenced := map[string]string{}
	note := func(path string) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		for _, png := range pngReference.FindAllString(string(raw), -1) {
			if _, ok := referenced[png]; !ok {
				referenced[png] = filepath.ToSlash(rel)
			}
		}
	}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			note(path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	note(filepath.Join(root, "README.md"))
	data, err := filepath.Glob(filepath.Join(root, "_data", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range data {
		note(path)
	}

	raw, err := os.ReadFile(filepath.Join(root, "scripts", "screenshots.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(raw), "\r\n", "\n")
	shots := map[string]bool{}
	for _, m := range shotTarget.FindAllStringSubmatch(script, -1) {
		shots[m[1]] = true
	}
	if len(referenced) == 0 || len(shots) == 0 {
		t.Fatalf("found %d referenced screenshots and %d Screenshot lines; the patterns are stale",
			len(referenced), len(shots))
	}

	for _, png := range sortedKeys(referenced) {
		if _, err := os.Stat(filepath.Join(root, "docs", "assets", png)); err != nil {
			t.Errorf("%s references docs/assets/%s, which does not exist", referenced[png], png)
		}
		if !shots[png] {
			t.Errorf("%s references %s, which no Screenshot in scripts/screenshots.sh writes", referenced[png], png)
		}
	}
	for _, png := range sortedKeys(shots) {
		if _, ok := referenced[png]; !ok {
			t.Errorf("scripts/screenshots.sh captures %s, which nothing under docs/, README.md or _data/ references", png)
		}
	}

	calls := tapeCall.FindAllStringSubmatchIndex(script, -1)
	if len(calls) == 0 {
		t.Fatal("no tape call found in scripts/screenshots.sh; the pattern is stale")
	}
	for i, c := range calls {
		name := script[c[2]:c[3]]
		end := len(script)
		if i+1 < len(calls) {
			end = calls[i+1][0]
		}
		wrote := false
		for _, m := range shotTarget.FindAllStringSubmatch(script[c[0]:end], -1) {
			if m[1] == name+".png" {
				wrote = true
			}
		}
		if !wrote {
			t.Errorf("tape %s never takes a Screenshot of %s.png, which tape() requires", name, name)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
