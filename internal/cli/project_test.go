package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// patchJSON parses args with `project edit`'s real flag set and returns the
// body projectPatchFromFlags would send, marshalled the way apiclient sends it.
func patchJSON(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newProjectEditCmd()
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	req, err := projectPatchFromFlags(cmd)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal %v: %v", args, err)
	}
	return string(body), nil
}

// TestProjectPatchFromFlagsSendsOnlyWhatWasSet is task 105's missing / null /
// set contract seen from the command line: a flag that was not given is a
// field left absent, an empty optional value is null, and everything else is
// sent as typed for the daemon to judge.
func TestProjectPatchFromFlagsSendsOnlyWhatWasSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"name", []string{"--name", "renamed"}, `{"name":"renamed"}`},
		{"path", []string{"--path", "/srv/repo"}, `{"path":"/srv/repo"}`},
		{"relative path as typed", []string{"--path", "repo"}, `{"path":"repo"}`},
		{"default branch", []string{"--default-branch", "develop"}, `{"default_branch":"develop"}`},
		{"workflow", []string{"--workflow", "review"}, `{"default_workflow":"review"}`},
		{"cap", []string{"--max-parallel", "3"}, `{"max_parallel_tasks":3}`},
		{"branch template", []string{"--branch-template", "feat/{{.ID}}"}, `{"branch_template":"feat/{{.ID}}"}`},

		// Clearing (decision 1): empty and whitespace-only both mean null.
		{"clear workflow", []string{"--workflow", ""}, `{"default_workflow":null}`},
		{"clear cap", []string{"--max-parallel", ""}, `{"max_parallel_tasks":null}`},
		{"clear branch template", []string{"--branch-template", ""}, `{"branch_template":null}`},
		{"blank workflow", []string{"--workflow", "  "}, `{"default_workflow":null}`},
		{"blank cap", []string{"--max-parallel", " \t"}, `{"max_parallel_tasks":null}`},
		{"blank branch template", []string{"--branch-template", " "}, `{"branch_template":null}`},

		// No clear form for the required fields: the daemon refuses these.
		{"empty name", []string{"--name", ""}, `{"name":""}`},
		{"empty path", []string{"--path", ""}, `{"path":""}`},
		{"empty default branch", []string{"--default-branch", ""}, `{"default_branch":""}`},

		// An out-of-range cap is a number, and the daemon owns the range.
		{"zero cap", []string{"--max-parallel", "0"}, `{"max_parallel_tasks":0}`},
		{"negative cap", []string{"--max-parallel=-2"}, `{"max_parallel_tasks":-2}`},

		{"repoint", []string{"--path", "/srv/other", "--default-branch", "trunk"}, `{"path":"/srv/other","default_branch":"trunk"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := patchJSON(t, tc.args...)
			if err != nil {
				t.Fatalf("projectPatchFromFlags(%q): %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("body for %q = %s, want %s", tc.args, got, tc.want)
			}
		})
	}
}

func TestProjectPatchFromFlagsRefusesBeforeSending(t *testing.T) {
	for _, args := range [][]string{{}, {"--json"}} {
		_, err := patchJSON(t, args...)
		if !errors.Is(err, errEmptyProjectPatch) {
			t.Errorf("projectPatchFromFlags(%q) = %v, want the empty-patch refusal", args, err)
		}
	}
	// The refusal is the user's only hint at what edit can change.
	for _, flag := range projectEditFlags {
		if !strings.Contains(errEmptyProjectPatch.Error(), "--"+flag) {
			t.Errorf("the empty-patch refusal does not name --%s: %q", flag, errEmptyProjectPatch)
		}
	}

	for _, v := range []string{"three", "1.5", "3x"} {
		_, err := patchJSON(t, "--name", "kept", "--max-parallel", v)
		if err == nil || !strings.Contains(err.Error(), "--max-parallel") {
			t.Errorf("--max-parallel %q: err %v, want a refusal naming the flag", v, err)
		}
	}
}
