package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/jira/fakejira"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

const liveJiraToken = "jira-live-secret-41d0b2"

// TestProjectJiraOverTheWire round-trips GET /v1/projects/{id}/jira through
// the client against the real handlers and the fake Jira (task 142.12):
// every field the client declares is one the server writes, and neither an
// answer nor an error carries the token.
func TestProjectJiraOverTheWire(t *testing.T) {
	t.Parallel()
	scenario := filepath.Join(t.TempDir(), "scenario")
	fj := httptest.NewServer(&fakejira.Store{ScenarioFile: scenario, Token: liveJiraToken})
	t.Cleanup(fj.Close)

	st, err := storetest.Open(filepath.Join(t.TempDir(), "jira.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &store.Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main", IssueSource: store.IssueSource{
		Provider: store.IssueProviderJira, JiraSite: fj.URL, JiraProjectKey: "VIN",
	}}
	if err := st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Environ: func() []string {
			return []string{"JIRA_USER=" + fakejira.DefaultUser, "JIRA_API_TOKEN=" + liveJiraToken}
		},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	c := apiclient.New(ts.URL, testToken)
	ctx := t.Context()

	got, err := c.ProjectJira(ctx, p.ID)
	if err != nil {
		t.Fatalf("ProjectJira: %v", err)
	}
	if !got.Enabled || !got.Available || got.Reason != "" || got.Unavailable() != "" ||
		got.Site != fj.URL || got.ProjectKey != "VIN" || got.ProjectID != "10000" ||
		got.Server == nil || got.Server.DeploymentType != "Cloud" || got.Server.Version == "" ||
		got.Account == nil || got.Account.DisplayName == "" {
		t.Errorf("ProjectJira = %+v", got)
	}

	if err := os.WriteFile(scenario, []byte(fakejira.ScenarioUnauthorized), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = c.ProjectJira(ctx, p.ID)
	if err != nil || got.Available || got.Reason != "unauthorized" || got.Unavailable() == "" ||
		got.Server != nil || got.Account != nil {
		t.Errorf("ProjectJira unauthorized = %+v, %v", got, err)
	}
	if strings.Contains(got.Unavailable(), liveJiraToken) {
		t.Error("the unavailability message carries the token")
	}

	_, err = c.ProjectJira(ctx, 9999)
	var e *apiclient.Error
	if !errors.As(err, &e) || e.Status != http.StatusNotFound {
		t.Errorf("ProjectJira unknown project = %v, want 404", err)
	}
	if err != nil && strings.Contains(err.Error(), liveJiraToken) {
		t.Error("the client error carries the token")
	}
}
