package workflow

import (
	"strings"
	"testing"
)

// The built-ins' version-coupled checklists carry Jira-imported issues
// (task 142.17): update-workflows in both of its branches, since the
// checklist is shared, and update-triggers. The phrases are single-line
// fragments so a reflow of the prose does not break them.
func TestBuiltinChecklistsCoverJiraIssues(t *testing.T) {
	render := func(name, id string, fields map[string]string) string {
		t.Helper()
		got, err := Render("prompt", stepByID(t, builtins()[name].Workflow.Steps, id).Prompt, projectRunContext(fields))
		if err != nil {
			t.Fatalf("Render(%s/%s) error = %v", name, id, err)
		}
		return got
	}
	workflowLines := []string{
		"19. Issues from Jira.",
		"--remote",
		"tests .Issue.Source.Provider",
		".Issue.Source.Key,",
		"$VINCENT_ISSUE_KEY or source.key",
		"Never assume number is set",
		"whose number is null for a",
		"no Jira REST call",
	}
	for branch, fields := range map[string]map[string]string{
		"project": nil,
		"global":  {"global": "true"},
	} {
		prompt := render(UpdateWorkflowsName, "modernize", fields)
		for _, want := range workflowLines {
			if !strings.Contains(prompt, want) {
				t.Errorf("update-workflows (%s) lacks %q", branch, want)
			}
		}
	}

	prompt := render(UpdateTriggersName, "propose", nil)
	for _, want := range []string{
		"21. Issues from Jira.",
		"by: sync may be Jira's importer",
		"stays untrusted for both",
		".Event.Issue.Source.Provider",
		"--source jira lists them",
		"a Jira binding alone imports nothing",
		"from the issue's tracker",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("update-triggers lacks %q", want)
		}
	}
	// A binding is not an import (review F3): until the Jira importer has
	// run, an issues trigger on a bound project never fires, so no built-in
	// may migrate a working poll script on the binding alone.
	for _, stale := range []string{"for a project bound to Jira issue sync becomes", "already imports its issues"} {
		if strings.Contains(prompt, stale) {
			t.Errorf("update-triggers still says %q", stale)
		}
	}
}

// No built-in still calls .Issue.Source a GitHub reference: an imported
// issue's tracker may be Jira (task 142 decision 11.3).
func TestBuiltinsDoNotCallSourceGitHubOnly(t *testing.T) {
	for name, src := range map[string]string{
		AdhocName:           AdhocSource,
		CreateWorkflowName:  CreateWorkflowSource,
		UpdateWorkflowsName: UpdateWorkflowsSource,
		CreateTriggerName:   CreateTriggerSource,
		UpdateTriggersName:  UpdateTriggersSource,
	} {
		for _, stale := range []string{"the GitHub reference", "never with `gh issue view`", "arriving by sync from GitHub"} {
			if strings.Contains(src, stale) {
				t.Errorf("%s still says %q", name, stale)
			}
		}
	}
}
