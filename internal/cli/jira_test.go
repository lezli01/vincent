package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/jira/fakejira"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

const cliJiraToken = "jira-cli-secret-9e2a77"

// `vincent jira status` against the real handler and the fake Jira (task
// 142.12): the human and --json renderings, a failed check, an unknown
// project, and the token in none of it.
func TestJiraStatusAgainstTheRealHandler(t *testing.T) {
	scenario := filepath.Join(t.TempDir(), "scenario")
	fj := httptest.NewServer(&fakejira.Store{ScenarioFile: scenario, Token: cliJiraToken})
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

	dataDir := t.TempDir()
	token, err := daemon.EnsureToken(dataDir)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Environ: func() []string {
			return []string{"JIRA_USER=" + fakejira.DefaultUser, "JIRA_API_TOKEN=" + cliJiraToken}
		},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	publishDaemon(t, dataDir, ts.URL)
	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv(config.EnvDataDir, dataDir)

	run := func(args ...string) (string, int) {
		t.Helper()
		var buf bytes.Buffer
		root := newRootCmd()
		root.SilenceErrors = true
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(append([]string{"jira", "status"}, args...))
		code := asExitCode(root.ExecuteContext(context.Background()))
		out := buf.String()
		if strings.Contains(out, cliJiraToken) {
			t.Errorf("jira status %v printed the token: %s", args, out)
		}
		return out, code
	}
	id := strconv.FormatInt(p.ID, 10)

	out, code := run("--project", id)
	for _, want := range []string{"enabled", "yes", fj.URL, "VIN", "Cloud", "10000", "available"} {
		if !strings.Contains(out, want) {
			t.Errorf("jira status: missing %q in %s", want, out)
		}
	}
	if code != 0 || strings.Contains(out, "unavailable") {
		t.Errorf("jira status: code %d, out %s", code, out)
	}

	out, code = run("--project", id, "--json")
	var got apiclient.JiraStatus
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 || !got.Available || got.ProjectID != "10000" {
		t.Errorf("jira status --json: code %d, %+v, %v (%s)", code, got, err, out)
	}

	if err := os.WriteFile(scenario, []byte(fakejira.ScenarioUnauthorized), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code = run("--project", id)
	if code != 0 || !strings.Contains(out, "unavailable: ") || strings.Contains(out, "Cloud") {
		t.Errorf("jira status unauthorized: code %d, out %s", code, out)
	}

	if out, code := run("--project", "9999"); code != 1 || !strings.Contains(out, "Error:") {
		t.Errorf("jira status unknown project: code %d, out %s", code, out)
	}
	if out, code := run(); code == 0 {
		t.Errorf("jira status without --project: code 0, out %s", out)
	}
}

func TestJiraCommandIsRegistered(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"jira", "status"})
	if err != nil || cmd.Name() != "status" || cmd.Parent().Name() != "jira" {
		t.Fatalf("jira status not found: %v", err)
	}
	if cmd.Flags().Lookup("project") == nil || cmd.Flags().Lookup("json") == nil {
		t.Error("jira status lacks --project or --json")
	}
}
