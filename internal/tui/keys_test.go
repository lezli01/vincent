package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
	"github.com/lezli01/vincent/internal/taskstate"
)

// Task 118. The registry became the dispatch source, and internal/keymap
// became the definition of which of its rows a user may move. These tests hold
// the two together — a catalog that disagrees with the registry would let
// config accept a keymap the TUI cannot honour, or refuse one it could.

// probeRemap redirects registryKey from a row's default key to its
// operation's effective one, for the rebound walk in TestEveryPanelKeyIsHandled.
var probeRemap map[string]string

// reboundKeys moves every term a panel probe reaches onto a key nothing else
// in the TUI answers. It must pass keymap.Build, which is itself asserted.
var reboundKeys = map[string]string{
	"refresh":      "f5",
	"archive":      "f6",
	"delete":       "f7",
	"draft_remove": "ctrl+k",
	"add":          "ctrl+n",
	"editor":       "ctrl+e",
	"free_text":    "ctrl+w",
	"browser":      "ctrl+b",
	"open_row":     "f2",
	"scope":        "f3",
	"filter":       "ctrl+f",
	"lane":         "f4",
	"new":          "f8",
	"repair":       "f9",
	"follow_up":    "f10",
	"edit_retry":   "f11",
	"help":         "f12",
	"palette":      "ctrl+a",
}

// withKeymap installs overrides for one test and restores the defaults after.
func withKeymap(t *testing.T, overrides map[string]string) {
	t.Helper()
	km, err := keymap.Build(overrides)
	if err != nil {
		t.Fatalf("test keymap refused: %v", err)
	}
	setKeymap(km)
	t.Cleanup(func() { setKeymap(keymap.Default()) })
}

func TestReboundKeysPassTheChecker(t *testing.T) {
	if _, err := keymap.Build(reboundKeys); err != nil {
		t.Fatal(err)
	}
}

// TestRegistryAgreesWithTheCatalog: every row that performs an operation
// carries the catalog's default key, every context a term's rows live in is
// one the catalog lists for it (and the other way round), and every row with
// no operation is a fixed key the catalog records — so config's checker sees
// exactly the keys `?` does.
func TestRegistryAgreesWithTheCatalog(t *testing.T) {
	registrySurfaces := map[keymap.Op]map[keymap.Surface]bool{}
	fixed := map[string]bool{}
	for _, f := range keymap.FixedKeys() {
		fixed[string(f.Surface)+"\x00"+f.Key] = true
	}
	for _, b := range bindings {
		if b.key == "" {
			continue
		}
		name := vocabularyRowName(b)
		if b.op == "" {
			surface := string(b.context)
			if b.scope == scopeGlobal {
				surface = string(keymap.Global)
			}
			keys := []string{b.key}
			if len(b.key) > 1 && strings.Contains(b.key, "/") {
				keys = strings.Split(b.key, "/")
			}
			for _, k := range keys {
				if !fixed[surface+"\x00"+k] {
					t.Errorf("%s: a fixed row keymap.fixed does not record — an override could shadow it", name)
				}
			}
			continue
		}
		info, ok := keymap.Lookup(b.op)
		if !ok {
			t.Errorf("%s: performs %q, which the catalog does not have", name, b.op)
			continue
		}
		if b.key != info.Default {
			t.Errorf("%s: default key %q, the catalog says %q", name, b.key, info.Default)
		}
		if b.hint != "" && !strings.HasPrefix(b.hint, b.key+" ") {
			t.Errorf("%s: hint %q does not start with its key — rebindHint cannot rewrite it", name, b.hint)
		}
		if b.scope == scopePanel {
			if registrySurfaces[b.op] == nil {
				registrySurfaces[b.op] = map[keymap.Surface]bool{}
			}
			registrySurfaces[b.op][keymap.Surface(b.context)] = true
		}
	}
	for _, info := range keymap.Catalog() {
		if info.Kind != keymap.KindTerm {
			continue
		}
		listed := map[keymap.Surface]bool{}
		for _, s := range info.Surfaces {
			if s == keymap.Actions {
				continue
			}
			listed[s] = true
			if !registrySurfaces[info.Op][s] {
				t.Errorf("%s: the catalog lists %q, where the registry has no row for it", info.Op, s)
			}
		}
		for s := range registrySurfaces[info.Op] {
			if !listed[s] {
				t.Errorf("%s: the registry has a row on %q the catalog does not list", info.Op, s)
			}
		}
	}
}

// TestActionsLiveIsTheFSMs holds the catalog's per-context "actions live"
// fact against taskstate rather than trusting it (task 118 decision 3): the
// home contexts offer actions, and the archived boards offer none because an
// archived task has no human action.
func TestActionsLiveIsTheFSMs(t *testing.T) {
	if got := taskstate.HumanActionsFrom(taskstate.Archived); len(got) != 0 {
		t.Fatalf("an archived task now offers %v — keymap's actionsLive has to change", got)
	}
	contexts := map[bindingContext]bool{}
	for _, b := range bindings {
		if b.scope == scopePanel {
			contexts[b.context] = true
		}
	}
	for ctx := range contexts {
		if got, want := keymap.ActionsLive(keymap.Surface(ctx)), isHomeContext(ctx); got != want {
			t.Errorf("%s: keymap says actions live = %v, the TUI routes them = %v", ctx, got, want)
		}
	}
}

// TestShippedRegistryPassesTheChecker is the registry's side of decision 4:
// the three vocabulary tests in bindings_test.go read the registry, and this
// one runs the function config runs.
func TestShippedRegistryPassesTheChecker(t *testing.T) {
	if err := keymap.Check(keymap.Default()); err != nil {
		t.Fatal(err)
	}
}

// TestEveryMatchedKeyIsRegistered is the converse 093 left out of reach: every
// literal key a handler switches on is either a fixed key the catalog records
// or a key no operation owns by default. A new `case "R":` fails here — it
// would keep answering R after a user moved refresh.
func TestEveryMatchedKeyIsRegistered(t *testing.T) {
	fixed := map[string]bool{}
	for _, f := range keymap.FixedKeys() {
		fixed[f.Key] = true
	}
	defaults := map[string]keymap.Op{}
	for _, info := range keymap.Catalog() {
		defaults[info.Default] = info.Op
	}
	// Keys a text field or a multi-key set answers that are not a registry
	// row's own spelling.
	always := map[string]bool{" ": true}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var problems []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if fd, ok := n.(*ast.FuncDecl); ok && (fd.Name.Name == "synthKey" || fd.Name.Name == "descend") {
				return false
			}
			sw, ok := n.(*ast.SwitchStmt)
			if !ok || !isKeySwitch(sw) {
				return true
			}
			for _, st := range sw.Body.List {
				for _, e := range st.(*ast.CaseClause).List {
					lit, ok := e.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					key, _ := strconv.Unquote(lit.Value)
					pos := fset.Position(lit.Pos())
					switch op, owned := defaults[key]; {
					case always[key]:
					case owned && !fixed[key]:
						problems = append(problems, pos.String()+": matches "+strconv.Quote(key)+
							", the "+string(op)+" operation's default — use opKey(keymap."+string(op)+")")
					case !owned && !fixed[key]:
						problems = append(problems, pos.String()+": matches "+strconv.Quote(key)+
							", which keymap.fixed does not record")
					}
				}
			}
			return true
		})
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

func isKeySwitch(sw *ast.SwitchStmt) bool {
	switch tag := sw.Tag.(type) {
	case *ast.CallExpr:
		sel, ok := tag.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "String"
	case *ast.Ident:
		return tag.Name == "key"
	}
	return false
}

// TestReboundKeysRender: under a non-default keymap no registry row, footer
// hint, palette entry or popup key line names a rebound operation's default
// key (decision 8).
func TestReboundKeysRender(t *testing.T) {
	withKeymap(t, reboundKeys)

	for _, b := range registry() {
		if b.op == "" || !currentKeys.Load().km.Overridden(b.op) {
			continue
		}
		info, _ := keymap.Lookup(b.op)
		if b.key == info.Default {
			t.Errorf("%s: still keyed %q", vocabularyRowName(b), b.key)
		}
		if strings.HasPrefix(b.hint, info.Default+" ") {
			t.Errorf("%s: hint %q still names %q", vocabularyRowName(b), b.hint, info.Default)
		}
	}
	for _, e := range paletteEntries(ctxChats, taskActions{}, false, true, true, nil) {
		if e.key == "R" || e.key == "/" || e.key == "enter" || e.key == "n" {
			t.Errorf("palette entry %q still keyed %q", e.label, e.key)
		}
	}
	for _, s := range footerPinnedSegs(false) {
		if s.key == ":" || s.key == "?" {
			t.Errorf("footer pinned segment still keyed %q", s.key)
		}
	}
	lines := map[string]string{
		"repair form":    repairFormFixture().render(100, 40),
		"follow-up form": followUpFormFixture().render(100, 40),
	}
	for name, out := range lines {
		if strings.Contains(out, " e $EDITOR") {
			t.Errorf("%s still prints `e $EDITOR`:\n%s", name, out)
		}
		if !strings.Contains(out, "ctrl+e $EDITOR") {
			t.Errorf("%s does not print the rebound editor key:\n%s", name, out)
		}
	}
	if h := helpText(ctxChats, true); strings.Contains(h, "repair form (R ") || !strings.Contains(h, "f5") {
		t.Errorf("help does not render the rebound keys:\n%s", h)
	}
}

// TestReboundKeyReplacesTheDefault: an override is not an alias (decision 6).
// The vacated key does nothing for the operation it used to perform.
func TestReboundKeyReplacesTheDefault(t *testing.T) {
	withKeymap(t, map[string]string{"filter": "ctrl+f", "archive": "f6"})

	v := chatsFixture()
	v.updateKey(registryKey(t, "/"))
	if v.filtering {
		t.Fatal("/ still opens the chats filter after filter moved to ctrl+f")
	}
	v.updateKey(registryKey(t, "ctrl+f"))
	if !v.filtering {
		t.Fatal("ctrl+f does not open the chats filter")
	}

	target := taskActions{id: 3, actions: []string{apiclient.ActionArchive}}
	if _, ok := resolveAction("A", target); ok {
		t.Fatal("A still resolves to archive after archive moved to f6")
	}
	if action, ok := resolveAction("f6", target); !ok || action != apiclient.ActionArchive {
		t.Fatalf("f6 resolves to %q, %v; want archive", action, ok)
	}
}

// TestSynthKeyRoundTripsAnyBindableKey: the palette and the footer replay a
// row's key through synthKey, so every shape keymap.ParseKey accepts has to
// come back as the press it names.
func TestSynthKeyRoundTripsAnyBindableKey(t *testing.T) {
	for _, key := range []string{"R", "ctrl+e", "alt+r", "f5", "home", "pgdown", "shift+tab", "left", "delete", "alt+f2"} {
		if _, err := keymap.ParseKey(key); err != nil {
			t.Fatalf("%q: %v", key, err)
		}
		if got := synthKey(key).String(); got != key {
			t.Errorf("synthKey(%q) presses as %q", key, got)
		}
	}
}
