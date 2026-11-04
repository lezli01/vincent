package tui

import (
	"context"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestProjectFormBindsIssueSourceLive edits a project's issue source in the
// project form against the real handlers (task 142.6): bind to jira, see the
// daemon's normalised binding come back, see a refusal parked on its row,
// then unbind to derive.
func TestProjectFormBindsIssueSourceLive(t *testing.T) {
	h := newProjectSlotsHarness(t, nil)
	client := apiclient.New(h.url, h.token)
	ctx := context.Background()
	get := func() apiclient.Project {
		t.Helper()
		p, err := client.GetProject(ctx, h.projectID)
		if err != nil {
			t.Fatalf("GetProject: %v", err)
		}
		return p
	}
	save := func(f *projectForm) (apiclient.Project, error) {
		t.Helper()
		msg, ok := f.submit()().(projectSavedMsg)
		if !ok {
			t.Fatal("submit did not answer projectSavedMsg")
		}
		return msg.project, msg.err
	}

	// The harness wires the API no git runner, and a server that cannot read
	// origin presumes a derived project GitHub's (task 142.7, issueResolution)
	// rather than concluding it is not.
	p := get()
	if p.IssueSource.Provider != apiclient.IssueProviderDerive || p.IssueSource.Effective != apiclient.IssueProviderGitHub {
		t.Fatalf("new project's issue source = %+v, want derive/github", p.IssueSource)
	}
	f := newProjectForm(client, &p)
	f.source = apiclient.IssueProviderJira
	f.jiraSite.SetValue("https://Acme.atlassian.net/")
	f.jiraKey.SetValue("ENG")
	got, err := save(f)
	if err != nil {
		t.Fatalf("bind jira: %v", err)
	}
	if j := got.IssueSource.Jira; got.IssueSource.Effective != "jira" || j == nil ||
		j.Site != "https://acme.atlassian.net" || j.ProjectKey != "ENG" {
		t.Fatalf("bound = %+v", got.IssueSource)
	}

	p = get()
	f = newProjectForm(client, &p)
	f.jiraSite.SetValue("http://acme.atlassian.net")
	if _, err := save(f); err == nil {
		t.Fatal("an http site on a public host was accepted")
	} else {
		f.applyFailure(err)
	}
	if f.cursor != pfJiraSite || f.rowErr[pfJiraSite] == "" {
		t.Errorf("refusal parked on row %v (%v), want the jira site row", f.cursor, f.rowErr)
	}

	p = get()
	f = newProjectForm(client, &p)
	f.source = apiclient.IssueProviderDerive
	if got, err = save(f); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	if got.IssueSource.Provider != apiclient.IssueProviderDerive || got.IssueSource.Jira != nil {
		t.Errorf("after unbind = %+v, want derive", got.IssueSource)
	}
}
