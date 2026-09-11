package trigger

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/workflow"
)

// handWritten is a trigger an author wrote with every construct the
// byte-fidelity guarantee covers: a header comment, an inline comment, blank
// lines and a `|` block scalar.
const handWritten = `# Jira ready-for-dev, polled every five minutes.
# Owner: me.

id: jira
enabled: false  # flip once the script is tested
source:
  type: command
  project: 1
  poll_interval: 5m
  command: [vincent-jira-poll, --project=VIN]

action:
  type: create_task
  title: '{{ .Event.key }}'
  description: |
    Jira {{ .Event.key }}

    Acceptance criteria live in the ticket.
`

func writeFile(t *testing.T, dir, name, content string, perm os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStarterValidatesAndProposes(t *testing.T) {
	src := Starter(StarterSpec{ID: "new-one", Project: 3, Command: []string{"poll", "--x=1"}, Workflow: "feature-pr"})
	d, errs := Parse(src, "new-one")
	if len(errs) > 0 {
		t.Fatalf("starter does not validate: %v\n%s", errs, src)
	}
	if d.Enabled {
		t.Error("starter is enabled")
	}
	if bytes.Contains(src, []byte("on_fire")) {
		t.Errorf("starter writes an on_fire line:\n%s", src)
	}
	if d.EffectiveOnFire() != OnFirePropose || d.EffectivePermission() != PermissionRestricted {
		t.Errorf("starter resolves to %s/%s", d.EffectiveOnFire(), d.EffectivePermission())
	}
}

// TestPatchByteFidelity: an edit rewrites only the lines its ops touch —
// comments, blank lines, the block scalar and CRLF endings come back as they
// were.
func TestPatchByteFidelity(t *testing.T) {
	for _, crlf := range []bool{false, true} {
		name := "lf"
		content := handWritten
		if crlf {
			name = "crlf"
			content = strings.ReplaceAll(handWritten, "\n", "\r\n")
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeFile(t, dir, "jira.yaml", content, 0o600)
			w := NewWriter(dir)
			v, err := workflow.Version(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Patch("jira", v, []workflow.Op{
				{Kind: workflow.OpSet, Path: "source.poll_interval", Value: "10m"},
				{Kind: workflow.OpSet, Path: "on_fire", Value: "create"},
			}); err != nil {
				t.Fatalf("Patch: %v", err)
			}
			got, _ := os.ReadFile(path)
			nl := "\n"
			if crlf {
				nl = "\r\n"
			}
			want := strings.Replace(content, "poll_interval: 5m", "poll_interval: 10m", 1)
			head, _, _ := strings.Cut(want, "action:")
			if !bytes.HasPrefix(got, []byte(head)) {
				t.Errorf("untouched head changed:\n%q", got)
			}
			for _, keep := range []string{
				"# Jira ready-for-dev, polled every five minutes." + nl + "# Owner: me." + nl + nl,
				"enabled: false  # flip once the script is tested" + nl,
				"  description: |" + nl + "    Jira {{ .Event.key }}" + nl + nl + "    Acceptance criteria live in the ticket." + nl,
				"on_fire: create" + nl,
			} {
				if !bytes.Contains(got, []byte(keep)) {
					t.Errorf("result lost %q:\n%q", keep, got)
				}
			}
			if crlf && bytes.Contains(bytes.ReplaceAll(got, []byte("\r\n"), nil), []byte("\n")) {
				t.Errorf("CRLF file gained a bare LF:\n%q", got)
			}
		})
	}
}

// TestPatchRefusedLeavesBytes: a patch whose result does not validate — or
// that names a path that is not there — changes nothing on disk.
func TestPatchRefusedLeavesBytes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "jira.yaml", handWritten, 0o600)
	w := NewWriter(dir)
	v, _ := workflow.Version(path)
	for _, ops := range [][]workflow.Op{
		{{Kind: workflow.OpSet, Path: "source.poll_interval", Value: "10ms"}},
		{{Kind: workflow.OpSet, Path: "id", Value: "renamed"}},
		{{Kind: workflow.OpRemove, Path: "steps[3]"}},
	} {
		_, err := w.Patch("jira", v, ops)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Errorf("Patch(%v) = %v, want *InvalidError", ops, err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != handWritten {
			t.Fatalf("refused patch %v changed the file:\n%s", ops, got)
		}
	}
	var inv *InvalidError
	_, err := w.Patch("jira", v, []workflow.Op{{Kind: workflow.OpSet, Path: "source.poll_interval", Value: "10ms"}})
	if !errors.As(err, &inv) || len(inv.Errors) == 0 || inv.Errors[0].Path != "source.poll_interval" {
		t.Errorf("refusal not located at its field: %v", err)
	}
}

// TestStaleVersions: PATCH and DELETE with an old token are refused with the
// current one, and nothing changes.
func TestStaleVersions(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)
	v1, err := w.Create("t1", Starter(StarterSpec{ID: "t1", Project: 1}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Create("t1", Starter(StarterSpec{ID: "t1", Project: 1})); !errors.Is(err, ErrExists) {
		t.Errorf("second Create = %v, want ErrExists", err)
	}
	v2, err := w.Patch("t1", v1, []workflow.Op{{Kind: workflow.OpSet, Path: "enabled", Value: "true"}})
	if err != nil || v2 == v1 {
		t.Fatalf("Patch = %q, %v", v2, err)
	}
	var stale *StaleError
	if _, err := w.Patch("t1", v1, []workflow.Op{{Kind: workflow.OpSet, Path: "enabled", Value: "false"}}); !errors.As(err, &stale) || stale.Current != v2 {
		t.Errorf("stale Patch = %v, want *StaleError carrying %s", err, v2)
	}
	if err := w.Delete("t1", v1); !errors.As(err, &stale) || stale.Current != v2 {
		t.Errorf("stale Delete = %v, want *StaleError carrying %s", err, v2)
	}
	if err := w.Delete("t1", v2); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := w.Delete("t1", v2); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete of a gone file = %v, want ErrNotFound", err)
	}
	if _, err := w.Create("../x", nil); err == nil {
		t.Error("Create accepted an id that escapes the directory")
	}
}
