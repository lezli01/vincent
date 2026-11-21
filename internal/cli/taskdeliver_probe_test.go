package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestResolveWorkflowsProbeTaskDeliverHelp is review F8 on #1043: the
// resolve workflows' `fetch` refuses a vincent too old for their `deliver`
// step by matching `vincent task deliver --help` against a shell glob of
// quoted pieces. Each piece must appear, in order, in the help this binary
// prints — so a wording change to the flag's help cannot silently make
// every run fail at fetch, and the probe keeps naming `none`, the
// close_reason #1042 added.
func TestResolveWorkflowsProbeTaskDeliverHelp(t *testing.T) {
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"task", "deliver", "--help"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("task deliver --help: %v", err)
	}
	help := buf.String()
	probe := regexp.MustCompile(`case "\$\(vincent task deliver --help 2>&1\)" in\n\s*(\*"[^\n]*\*)\) ;;`)
	piece := regexp.MustCompile(`"([^"]*)"`)
	for _, rel := range []string{
		".vincent/workflows/github-resolve-issue.yaml",
		".vincent/workflows/github-resolve-issue-dag.yaml",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		m := probe.FindStringSubmatch(strings.ReplaceAll(string(raw), "\r\n", "\n"))
		if m == nil {
			t.Fatalf("%s: no task deliver probe found; the pattern is stale", rel)
		}
		pieces := piece.FindAllStringSubmatch(m[1], -1)
		if !strings.Contains(m[1], "none") {
			t.Errorf("%s: the probe %s does not check for close_reason none", rel, m[1])
		}
		rest := help
		for _, p := range pieces {
			i := strings.Index(rest, p[1])
			if i < 0 {
				t.Errorf("%s: probe piece %q is not in task deliver --help (in order):\n%s", rel, p[1], help)
				break
			}
			rest = rest[i+len(p[1]):]
		}
	}
}

// TestHandoffDeliverHelpSaysASuggestedNoneLeavesItOpen is review F10 on
// #1043: docs/reference/cli.md tracks the cobra tree, and since #1042 a
// delivery's suggested `none` leaves its issue open and moves it to the
// open lane. The command's own help must say so too.
func TestHandoffDeliverHelpSaysASuggestedNoneLeavesItOpen(t *testing.T) {
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"handoff", "deliver", "--help"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("handoff deliver --help: %v", err)
	}
	help := strings.Join(strings.Fields(buf.String()), " ")
	for _, want := range []string{"a delivery that suggested none", "open lane"} {
		if !strings.Contains(help, want) {
			t.Errorf("handoff deliver --help does not say %q:\n%s", want, buf.String())
		}
	}
}
