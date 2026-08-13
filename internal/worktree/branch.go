package worktree

import (
	"errors"
	"fmt"
	"strings"
	"text/template"
)

// ErrBranchNeedsID reports that a branch template referenced `.ID` while the
// task id was still unknown.
//
// Callers treat this as *routing information*, not a failure: it is the signal
// that this template can only be rendered once the task row exists, so the name
// must be produced inside the insert transaction rather than validated before it
// (task 001). Detecting that with a method error rather than by walking the
// template parse tree is deliberate — a walk has to handle `with`, `range` and
// `$var` aliasing to be correct, and a detector that gets it wrong renders a
// silent `0` into somebody's branch name.
var ErrBranchNeedsID = errors.New("branch template references .ID, which is unknown until the task row exists")

// BranchProject is `.Project` inside a branch template.
type BranchProject struct {
	Name          string
	Path          string
	DefaultBranch string
}

// BranchContext is the template context for a configurable branch name
// (task 001).
//
// It is deliberately *not* workflow.RenderContext. That type carries
// `.Task.BranchName`, `.Worktree.Path`, `.Step` and `.Steps` — real struct
// fields with valid zero values at task-creation time — so referencing one would
// render an empty string and quietly produce a branch like `feat/-retry`.
// text/template's `missingkey=error` guards *map* keys only, so the guard has to
// come from the type not having those fields at all. It also makes a template
// that refers to the very name it is computing unrepresentable.
type BranchContext struct {
	// id is nil until the task row exists; see ID.
	id *int64

	Title      string
	Slug       string
	BaseBranch string
	Fields     map[string]string
	Project    BranchProject
}

// NewBranchContext returns a context for a task whose id is not yet known.
// Slug is derived from title with the §5.3 rules, so `{{.Slug}}` means the same
// thing in a template as it does in the built-in name.
func NewBranchContext(title, baseBranch string, fields map[string]string, project BranchProject) BranchContext {
	return BranchContext{
		Title:      title,
		Slug:       Slug(title),
		BaseBranch: baseBranch,
		Fields:     fields,
		Project:    project,
	}
}

// WithID returns a copy of c whose ID resolves to id.
func (c BranchContext) WithID(id int64) BranchContext {
	c.id = &id
	return c
}

// ID is `.ID` in a branch template. It is a method, not a field, so that
// referencing the task id before the row exists is an error the renderer
// propagates rather than a zero that silently becomes part of a branch name.
func (c BranchContext) ID() (int64, error) {
	if c.id == nil {
		return 0, ErrBranchNeedsID
	}
	return *c.id, nil
}

// branchFuncs are the functions a branch template may call. `slug` applies the
// §5.3 rules to any value, so a name can be built from a field carrying spaces
// or punctuation: {{ slug (index .Fields "ticket") }}.
var branchFuncs = template.FuncMap{"slug": Slug}

// ValidateBranchTemplate reports whether text parses as a branch template. It is
// what lets a broken template fail where it was written — config load, or a
// project update — instead of at every task creation afterwards.
func ValidateBranchTemplate(text string) error {
	_, err := parseBranchTemplate(text)
	return err
}

func parseBranchTemplate(text string) (*template.Template, error) {
	tmpl, err := template.New("branch").Funcs(branchFuncs).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("parse branch template: %w", err)
	}
	return tmpl, nil
}

// RenderBranch renders a branch-name template against c and returns the name
// with surrounding whitespace removed, so a YAML block scalar's trailing newline
// does not become an illegal ref.
//
// Unknown fields and missing map keys read through field syntax
// (`{{.Fields.ticket}}`) are errors, matching the phase 2 decision behind
// workflow.Render: a typo fails loudly instead of rendering a hole. Note that
// `missingkey=error` does *not* cover the `index` builtin — as
// workflow.TaskContext documents, `index` is the way to read an *optional* field
// — so `{{ index .Fields "ticket" }}` on a task without one renders nothing and
// yields a name like `feat/-fix-login`, which git happily accepts. Branch
// templates should therefore prefer field syntax and reach for
// `{{ with index … }}` when a segment is genuinely optional.
//
// When the template needs the task id and c carries none, the error *is*
// ErrBranchNeedsID — unwrapped, because at that point it is a routing signal and
// the template machinery's wrapping ("executing … at <.ID>: error calling ID")
// only obscures it in a log.
func RenderBranch(text string, c BranchContext) (string, error) {
	tmpl, err := parseBranchTemplate(text)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, c); err != nil {
		if errors.Is(err, ErrBranchNeedsID) {
			return "", ErrBranchNeedsID
		}
		return "", fmt.Errorf("render branch template: %w", err)
	}
	name := strings.TrimSpace(sb.String())
	if name == "" {
		// git would reject this too, but "'' is not a valid branch name" says
		// nothing about which template produced it.
		return "", fmt.Errorf("branch template %q rendered an empty name", text)
	}
	return name, nil
}
