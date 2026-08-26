package workflow

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/examples"
	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/config"
)

// curatedOptions is the validation context `vincent workflow validate` uses:
// curated catalogs only, so nothing here spawns a probe or depends on which
// agent CLIs the machine happens to have, and the built-in loop ceiling, so a
// file that validates on a laptop validates on a CI runner with no config.
func curatedOptions() Options {
	reg := agent.NewRegistry(
		claude.New(func() string { return "" }),
		codex.New(func() string { return "" }),
		cursor.New(func() string { return "" }),
	)
	catalogs := make(agent.Catalogs, len(reg.Names()))
	for _, a := range reg.All() {
		catalogs[a.Name()] = a.Curated()
	}
	return Options{
		KnownAgents:   reg.Names(),
		Catalogs:      func() agent.Catalogs { return catalogs },
		MaxIterations: func() int { return config.Default().Loop.MaxIterations },
	}
}

// TestSkeletonValidates holds the file `workflow init` writes to the bar a
// shipped example is held to: zero errors *and* zero warnings. A starting
// point that arrives with a warning teaches the warning along with the
// schema, and the first thing its author would do is run `workflow validate`
// on it.
func TestSkeletonValidates(t *testing.T) {
	wf, warns, err := Parse([]byte(SkeletonSource), curatedOptions())
	if err != nil {
		t.Fatalf("skeleton does not validate: %v", err)
	}
	if len(warns) > 0 {
		t.Errorf("skeleton validates with warnings: %v", warns)
	}
	if wf.Name == "" || len(wf.Steps) != 1 {
		t.Fatalf("skeleton = %+v, want a name and exactly one step", wf)
	}
	if wf.Steps[0].Type != StepAgent {
		t.Errorf("skeleton step type = %q, want %q", wf.Steps[0].Type, StepAgent)
	}
	// The comments are the deliverable: this is what a first author reads
	// instead of the schema reference, so the parts they otherwise miss have
	// to be named in it.
	for _, want := range []string{
		"type: command", "type: manual", "check", "max_retries", "timeout",
		"docs/reference/workflow-schema.md",
	} {
		if !strings.Contains(SkeletonSource, want) {
			t.Errorf("skeleton never mentions %q", want)
		}
	}
}

// TestSkeletonRenames is the shape `workflow init` actually writes: the
// skeleton under the user's chosen name still validates, and is addressable
// under that name (the registry keys on name:, not the file name — §5.2).
func TestSkeletonRenames(t *testing.T) {
	src, err := SetName([]byte(SkeletonSource), "my-flow")
	if err != nil {
		t.Fatalf("SetName: %v", err)
	}
	wf, warns, err := Parse(src, curatedOptions())
	if err != nil {
		t.Fatalf("renamed skeleton does not validate: %v", err)
	}
	if len(warns) > 0 {
		t.Errorf("renamed skeleton validates with warnings: %v", warns)
	}
	if wf.Name != "my-flow" {
		t.Errorf("name = %q, want my-flow", wf.Name)
	}
}

// TestEmbeddedExamplesRenameAndValidate is the `--from` guarantee, table-driven
// over the embedded filesystem rather than a list beside it: a new example
// cannot land as a broken `--from` value without failing here.
//
// TestShippedExamplesValidate already parses the files as published; this
// parses them *after* the rewrite, which is what init writes to disk.
func TestEmbeddedExamplesRenameAndValidate(t *testing.T) {
	names := examples.Names()
	if len(names) == 0 {
		t.Fatal("examples embeds nothing; this test would pass vacuously")
	}
	opts := curatedOptions()
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			src, err := examples.Read(name)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			renamed, err := SetName(src, "renamed-flow")
			if err != nil {
				t.Fatalf("SetName: %v", err)
			}
			wf, warns, err := Parse(renamed, opts)
			if err != nil {
				t.Fatalf("does not validate after rename: %v", err)
			}
			if len(warns) > 0 {
				t.Errorf("validates with warnings after rename: %v", warns)
			}
			if wf.Name != "renamed-flow" {
				t.Errorf("name = %q, want renamed-flow", wf.Name)
			}
			// Only the one line moved: the comment budget is why this is a
			// text edit rather than a parse and re-marshal, so a rewrite that
			// silently dropped a comment would defeat the whole feature.
			if got, want := strings.Count(string(renamed), "#"), strings.Count(string(src), "#"); got != want {
				t.Errorf("comment characters = %d, want %d", got, want)
			}
			if got, want := lineCount(renamed), lineCount(src); got != want {
				t.Errorf("line count = %d, want %d", got, want)
			}
		})
	}
}

func lineCount(b []byte) int { return strings.Count(string(b), "\n") }

// setNameFixture is deliberately adversarial: a header comment repeating the
// old name, a `- name:` under fields:, a name: nested inside a step mapping,
// and a name: inside a prompt: block scalar. Only the top-level key may move.
const setNameFixture = `# old-flow — a header comment that names the workflow.
#
# name: old-flow appears in this comment too.
name: old-flow
description: |
  A description whose second line reads
  name: not-the-key
fields:
  - name: ticket
    type: string
  - name: name
    type: string
steps:
  - id: run
    type: agent
    name: not-the-key-either
    prompt: |
      Write a file whose first line is:
      name: old-flow
      and do not change anything else.
`

// TestSetNameRewritesOnlyTheTopLevelKey: the rewrite is what makes an
// example reusable under another name, so it has to be exact. Everything but
// the single top-level `name:` line survives byte for byte.
func TestSetNameRewritesOnlyTheTopLevelKey(t *testing.T) {
	got, err := SetName([]byte(setNameFixture), "new-flow")
	if err != nil {
		t.Fatalf("SetName: %v", err)
	}
	want := strings.Replace(setNameFixture, "\nname: old-flow\n", "\nname: new-flow\n", 1)
	if string(got) != want {
		t.Errorf("SetName rewrote more than the top-level key:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	// Spelled out separately, because a diff of two long strings is easy to
	// misread and these four are the whole point.
	for _, survives := range []string{
		"# old-flow — a header comment that names the workflow.",
		"# name: old-flow appears in this comment too.",
		"  - name: ticket",
		"    name: not-the-key-either",
		"      name: old-flow\n",
	} {
		if !strings.Contains(string(got), survives) {
			t.Errorf("SetName touched %q", survives)
		}
	}
}

// TestSetNamePreservesCRLF: a workflow file authored on Windows is CRLF
// throughout, and a rewrite that normalized the one line it touches would
// leave a mixed-ending file behind.
func TestSetNamePreservesCRLF(t *testing.T) {
	src := "# header\r\nname: old\r\nsteps:\r\n  - id: run\r\n"
	got, err := SetName([]byte(src), "new")
	if err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if want := "# header\r\nname: new\r\nsteps:\r\n  - id: run\r\n"; string(got) != want {
		t.Errorf("SetName = %q, want %q", got, want)
	}
}

func TestSetNameRejects(t *testing.T) {
	if _, err := SetName([]byte("steps:\n  - id: run\n"), "x"); err == nil {
		t.Error("SetName accepted a source with no top-level name: key")
	}
	if _, err := SetName([]byte(setNameFixture), "a/b"); err == nil {
		t.Error("SetName accepted a name carrying a path separator")
	}
}

// TestDeclaredName: the registry keys on name:, so this is the question
// `workflow init` asks of every sibling before it writes. A file that does
// not parse has no knowable name, which is a different answer from "".
func TestDeclaredName(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"declared", "name: feature-pr\nsteps: []\n", "feature-pr"},
		{"absent", "steps: []\n", ""},
		{"unparseable", "name: [unterminated\n", ""},
		{"invalid but named", "name: feature-pr\nsteps: not-a-list\n", "feature-pr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeclaredName([]byte(tc.src)); got != tc.want {
				t.Errorf("DeclaredName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIsBuiltin guards the shadow warning's only fact: every built-in name
// answers true, so a third built-in is covered without a second edit.
func TestIsBuiltin(t *testing.T) {
	for name := range builtinSources {
		if !IsBuiltin(name) {
			t.Errorf("IsBuiltin(%q) = false", name)
		}
	}
	if IsBuiltin("definitely-not-a-builtin") {
		t.Error("IsBuiltin answered true for an unknown name")
	}
}
