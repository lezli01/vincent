package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// sourceJSON parses args with `project edit`'s flag set and returns the
// issue_source issueSourceFromFlags builds against current.
func sourceJSON(t *testing.T, current *apiclient.ProjectIssueSource, args ...string) (string, error) {
	t.Helper()
	cmd := newProjectEditCmd()
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	req, err := issueSourceFromFlags(cmd, current)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), nil
}

func TestIssueSourceFromFlags(t *testing.T) {
	mapping := filepath.Join(t.TempDir(), "m.json")
	if err := os.WriteFile(mapping, []byte(`{"overrides":{"reopen":"Again"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bound := &apiclient.ProjectIssueSource{Provider: "jira", Effective: "jira", Jira: &apiclient.ProjectIssueSourceJira{
		Site: "https://acme.atlassian.net", Projects: []apiclient.JiraProject{{Key: "ENG", JQL: "labels = a"}},
	}}
	several := &apiclient.ProjectIssueSource{Provider: "jira", Effective: "jira", Jira: &apiclient.ProjectIssueSourceJira{
		Site: "https://acme.atlassian.net", Projects: []apiclient.JiraProject{{Key: "ENG"}, {Key: "OPS"}},
	}}
	derived := &apiclient.ProjectIssueSource{Provider: "derive", Effective: "github"}
	for _, tc := range []struct {
		name    string
		current *apiclient.ProjectIssueSource
		args    []string
		want    string
	}{
		{"no flag", bound, []string{"--name", "x"}, `null`},
		{"none", derived, []string{"--issue-source", "none"}, `{"provider":"none"}`},
		{"empty is derive", bound, []string{"--issue-source", ""}, `{"provider":"derive"}`},
		{
			"bind jira", derived,
			[]string{"--issue-source", "jira", "--jira-site", "https://x.example", "--jira-project", "OPS", "--jira-mapping", mapping},
			`{"provider":"jira","jira":{"site":"https://x.example","projects":[{"key":"OPS","jql":""}],"mapping":{"overrides":{"reopen":"Again"}}}}`,
		},
		{
			"jql only keeps the rest", bound,
			[]string{"--jira-jql", "labels = b"},
			`{"provider":"jira","jira":{"site":"https://acme.atlassian.net","projects":[{"key":"ENG","jql":"labels = b"}],"mapping":null}}`,
		},
		{
			"several projects, each with its jql", bound,
			[]string{"--jira-project", "ENG", "--jira-project", " OPS : labels = \"a:b\" OR type = Bug", "--jira-project", "WEB:"},
			`{"provider":"jira","jira":{"site":"https://acme.atlassian.net","projects":[{"key":"ENG","jql":""},` +
				`{"key":"OPS","jql":"labels = \"a:b\" OR type = Bug"},{"key":"WEB","jql":""}],"mapping":null}}`,
		},
		{
			"--jira-project replaces the list", several,
			[]string{"--jira-project", "WEB"},
			`{"provider":"jira","jira":{"site":"https://acme.atlassian.net","projects":[{"key":"WEB","jql":""}],"mapping":null}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sourceJSON(t, tc.current, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("issue_source = %s\nwant %s", got, tc.want)
			}
		})
	}

	if _, err := sourceJSON(t, derived, "--jira-site", "https://x.example"); err == nil ||
		!strings.Contains(err.Error(), "--issue-source jira") {
		t.Errorf("a Jira flag on a non-jira project = %v, want a refusal naming --issue-source jira", err)
	}
	if _, err := sourceJSON(t, several, "--jira-jql", "labels = a"); err == nil ||
		!strings.Contains(err.Error(), "--jira-project KEY:JQL") {
		t.Errorf("--jira-jql on several projects = %v, want a refusal naming --jira-project KEY:JQL", err)
	}
	if bound.Jira.Projects[0].JQL != "labels = a" {
		t.Errorf("building the request edited the current binding: %+v", bound.Jira.Projects)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{nope`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceJSON(t, bound, "--jira-mapping", bad); err == nil {
		t.Error("a malformed mapping file was accepted")
	}
	// An issue-source flag alone is a change, so edit does not refuse it.
	if _, err := patchJSON(t, "--issue-source", "none"); err != nil {
		t.Errorf("edit with only --issue-source: %v", err)
	}
}
