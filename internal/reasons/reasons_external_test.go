package reasons_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/reasons"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// notATaskReason are the Reason* constants in internal/worktree that never
// land on a task or a step run, and so are deliberately absent from the
// catalogue (task 127 decision 1). Every constant is either explained or
// named here; a new one that is neither fails the test.
var notATaskReason = map[string]string{
	// API errors from opening a pull request (task 069).
	"push_rejected":      "ReasonPushRejected",
	"push_no_credential": "ReasonPushNoCredential",
	"push_failed":        "ReasonPushFailed",
	// gc's skip reason for an orphan it cannot judge (task 005).
	"dirty_unknown": "ReasonDirtyUnknown",
	// The chat API's (task 067 and after). repo_operation_in_progress was
	// one too, until an issue's main task began blocking on it (134.12).
	"workspace_path_missing": "ReasonWorkspacePathMissing",
}

// reasonConstants parses the non-test Go files of dir and returns every
// const named Reason* with its string value. A value spelled as another
// package's constant (taskrun.ReasonMergeConflict = worktree.ReasonMergeConflict)
// is resolved through aliases.
func reasonConstants(t *testing.T, dir string, aliases map[string]string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if !strings.HasPrefix(id.Name, "Reason") || i >= len(vs.Values) {
						continue
					}
					switch v := vs.Values[i].(type) {
					case *ast.BasicLit:
						if s, err := strconv.Unquote(v.Value); err == nil && v.Kind == token.STRING {
							out[id.Name] = s
						}
					case *ast.SelectorExpr:
						s, ok := aliases[v.Sel.Name]
						if !ok {
							t.Errorf("%s: %s aliases %s, which is not a known Reason constant", dir, id.Name, v.Sel.Name)
							continue
						}
						out[id.Name] = s
					}
				}
			}
		}
	}
	return out
}

func TestCatalogueCoversEveryTaskAndStepReason(t *testing.T) {
	root := ".."
	worktree := reasonConstants(t, filepath.Join(root, "worktree"), nil)
	taskrun := reasonConstants(t, filepath.Join(root, "taskrun"), worktree)
	if len(worktree) == 0 || len(taskrun) == 0 {
		t.Fatalf("parsed %d worktree and %d taskrun reasons; the parse found nothing", len(worktree), len(taskrun))
	}

	constants := map[string]bool{store.SkipReasonCondition: true}
	for _, v := range worktree {
		constants[v] = true
	}
	for _, v := range taskrun {
		constants[v] = true
	}

	for value, name := range notATaskReason {
		if worktree[name] != value {
			t.Errorf("excluded %s: worktree.%s is %q, want it to exist with that value", value, name, worktree[name])
		}
		if slices.Contains(reasons.Known(), value) {
			t.Errorf("excluded %s is in the catalogue too", value)
		}
	}
	for v := range constants {
		_, excluded := notATaskReason[v]
		if !excluded && !slices.Contains(reasons.Known(), v) {
			t.Errorf("reason %q is neither explained in internal/reasons nor excluded as not-a-task-reason", v)
		}
	}
	for _, k := range reasons.Known() {
		if !constants[k] {
			t.Errorf("catalogue entry %q is not a Reason* constant in taskrun or worktree, nor store.SkipReasonCondition", k)
		}
	}
	if t.Failed() {
		t.Logf("constants: %v", slices.Sorted(maps.Keys(constants)))
	}
}

// TestActionsAreHumanActions: every suggested action is one a person can
// take (§6), never an engine event such as admit or fail.
func TestActionsAreHumanActions(t *testing.T) {
	for _, r := range reasons.Known() {
		for _, a := range reasons.Explain(r).Actions {
			if !taskstate.Human(taskstate.Action(a)) {
				t.Errorf("%s: action %q is not a §6 human action", r, a)
			}
		}
	}
}
