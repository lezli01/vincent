package worktree

import (
	"errors"
	"strings"
	"testing"
)

func testCtx() BranchContext {
	return NewBranchContext("Fix login", "main",
		map[string]string{"ticket": "OPS-123", "messy": "Two Words!"},
		BranchProject{Name: "vincent", Path: "/repo", DefaultBranch: "main"})
}

// The whole routing design rests on this: text/template must propagate an error
// returned by a method so that errors.Is can recognise it through the
// ExecError wrapping. If this ever stops holding, RenderBranch silently starts
// reporting a generic render failure and the caller takes the wrong path.
func TestIDBeforeInsertIsRoutingSignal(t *testing.T) {
	_, err := RenderBranch(`vincent/{{.ID}}-{{.Slug}}`, testCtx())
	if !errors.Is(err, ErrBranchNeedsID) {
		t.Fatalf("err = %v, want it to wrap ErrBranchNeedsID", err)
	}
}

func TestWithIDRendersTheRealID(t *testing.T) {
	got, err := RenderBranch(`vincent/{{.ID}}-{{.Slug}}`, testCtx().WithID(42))
	if err != nil {
		t.Fatalf("RenderBranch: %v", err)
	}
	if want := "vincent/42-fix-login"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// WithID must not mutate the receiver: the pre-insert context is rendered first
// and has to stay usable (and id-less) afterwards.
func TestWithIDDoesNotMutateReceiver(t *testing.T) {
	base := testCtx()
	_ = base.WithID(7)
	if _, err := RenderBranch(`{{.ID}}`, base); !errors.Is(err, ErrBranchNeedsID) {
		t.Fatalf("receiver was mutated by WithID: err = %v", err)
	}
}

func TestRenderBranch(t *testing.T) {
	for _, tc := range []struct {
		name, tmpl, want string
	}{
		{"slug of the title", `feat/{{.Slug}}`, "feat/fix-login"},
		{"raw field", `feat/{{ index .Fields "ticket" }}`, "feat/OPS-123"},
		{"slug func over a field", `feat/{{ slug (index .Fields "messy") }}`, "feat/two-words"},
		{"base branch", `off/{{.BaseBranch}}`, "off/main"},
		{"project name", `{{.Project.Name}}/{{.Slug}}`, "vincent/fix-login"},
		{"title verbatim is legal here", `x/{{.Title}}`, "x/Fix login"},
		{"conditional, the faithful default shape", `vincent/{{with .Slug}}{{.}}{{end}}`, "vincent/fix-login"},
		{"surrounding whitespace is trimmed", "  feat/{{.Slug}}\n", "feat/fix-login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderBranch(tc.tmpl, testCtx())
			if err != nil {
				t.Fatalf("RenderBranch(%q): %v", tc.tmpl, err)
			}
			if got != tc.want {
				t.Fatalf("RenderBranch(%q) = %q, want %q", tc.tmpl, got, tc.want)
			}
		})
	}
}

// The point of a dedicated context: the fields that mean nothing at task
// creation must be *absent*, so referencing one fails instead of rendering the
// empty string that workflow.RenderContext would supply.
func TestRenderBranchRejectsReferencesThatWouldBeHoles(t *testing.T) {
	for _, tmpl := range []string{
		`{{.BranchName}}`,    // the name being computed — self-reference
		`{{.Worktree.Path}}`, // no worktree exists yet
		`{{.Step.ID}}`,       // no step is running
		`{{.Task.Title}}`,    // not the workflow context's shape
		`{{.Fields.nope}}`,   // missingkey=error on map field access
		`{{.Nope}}`,
	} {
		if got, err := RenderBranch(tmpl, testCtx()); err == nil {
			t.Errorf("RenderBranch(%q) = %q, want an error rather than a hole", tmpl, got)
		}
	}
}

// `missingkey=error` governs map access through *field* syntax, not the `index`
// builtin — which is why workflow.TaskContext documents `index` as the way to
// read an optional field. Both forms are available here for consistency with
// workflow templates, and the difference decides which one a branch template
// should use: `.Fields.ticket` fails loudly on a task with no ticket, while
// `index` renders nothing and git then *accepts* the result (`feat/-fix-login`
// is a legal ref). So the loud form is the documented default for branch
// templates, and `index` is for a segment you deliberately want optional.
func TestIndexIsTheQuietFormAndFieldAccessIsTheLoudOne(t *testing.T) {
	loud := `feat/{{.Fields.absent}}-{{.Slug}}`
	if _, err := RenderBranch(loud, testCtx()); err == nil {
		t.Errorf("RenderBranch(%q) should fail on a missing field", loud)
	}

	quiet := `feat/{{ index .Fields "absent" }}-{{.Slug}}`
	got, err := RenderBranch(quiet, testCtx())
	if err != nil {
		t.Fatalf("RenderBranch(%q): %v", quiet, err)
	}
	if want := "feat/-fix-login"; got != want {
		t.Fatalf("RenderBranch(%q) = %q, want %q — if this changed, the guidance "+
			"in the task 001 decisions and the config reference is stale", quiet, got, want)
	}

	// The intended way to make a segment optional without leaving a hole.
	conditional := `feat/{{ with index .Fields "absent" }}{{.}}-{{ end }}{{.Slug}}`
	got, err = RenderBranch(conditional, testCtx())
	if err != nil {
		t.Fatalf("RenderBranch(%q): %v", conditional, err)
	}
	if want := "feat/fix-login"; got != want {
		t.Fatalf("RenderBranch(%q) = %q, want %q", conditional, got, want)
	}
}

// An empty name is caught here rather than left to git, whose message for an
// empty branch name names no template and so cannot say which one produced it.
func TestRenderBranchRejectsAnEmptyResult(t *testing.T) {
	for _, tmpl := range []string{``, `{{ index .Fields "absent" }}`, `   `} {
		if got, err := RenderBranch(tmpl, testCtx()); err == nil {
			t.Errorf("RenderBranch(%q) = %q, want an error for an empty name", tmpl, got)
		}
	}
}

func TestValidateBranchTemplate(t *testing.T) {
	if err := ValidateBranchTemplate(`feat/{{.Slug}}-{{.ID}}`); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	// Unparseable, so it must fail where it was written rather than at every
	// task creation afterwards.
	err := ValidateBranchTemplate(`feat/{{.Slug`)
	if err == nil {
		t.Fatal("unterminated action accepted")
	}
	if !strings.Contains(err.Error(), "parse branch template") {
		t.Fatalf("error should name what failed, got %v", err)
	}
	// Parsing cannot know whether an id will be available, so a template that
	// needs one is still *valid* — that is a render-time routing question.
	if err := ValidateBranchTemplate(`{{.ID}}`); err != nil {
		t.Fatalf("id-bearing template rejected at parse time: %v", err)
	}
}

// The built-in default is a Go function (task 001 decision), but a template must
// be able to reproduce it — including the empty-slug edge case, where the naive
// form yields a trailing dash and the faithful one does not.
func TestDefaultShapeIsExpressibleAsATemplate(t *testing.T) {
	const faithful = `vincent/{{.ID}}{{with .Slug}}-{{.}}{{end}}`
	const naive = `vincent/{{.ID}}-{{.Slug}}`

	for _, title := range []string{"Fix login", "!!!", "", "---", "Añadir cosas"} {
		ctx := NewBranchContext(title, "main", nil, BranchProject{})
		got, err := RenderBranch(faithful, ctx.WithID(12))
		if err != nil {
			t.Fatalf("RenderBranch(%q): %v", title, err)
		}
		if want := BranchName(12, title); got != want {
			t.Errorf("faithful template for %q = %q, want %q", title, got, want)
		}
	}

	// And the naive form really is wrong, which is why the decision records it.
	got, err := RenderBranch(naive, NewBranchContext("!!!", "main", nil, BranchProject{}).WithID(12))
	if err != nil {
		t.Fatalf("RenderBranch: %v", err)
	}
	if got == BranchName(12, "!!!") {
		t.Fatal("naive template matched BranchName; the trailing-dash trap is gone and the decision note is stale")
	}
}
