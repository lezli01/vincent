package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// The definition endpoint's client model is a hand-written mirror of a
// hand-written server DTO, so the only test that proves anything is one that
// decodes what the real handler encodes. A fake server would be a copy of the
// same assumption, checked against itself.

const laneYAML = `name: shipit
description: fan out and merge
fields:
  - name: environment
    label: Environment
    type: string
    required: true
defaults:
  agent: claude
  model: sonnet
steps:
  - id: plan
    type: agent
    prompt: plan it
    check: go build ./...
  - id: spread
    type: fan_out
    lanes:
      - id: api
        steps:
          - {id: api_impl, type: agent, prompt: api}
      - id: web
        workflow: other
    merge:
      on_conflict: agent
      agent: {id: fixup, type: agent, prompt: fix}
  - id: repeat
    type: loop
    for_each: '{{ .Steps.plan.Result }}'
    steps:
      - {id: body, type: command, run: echo x}
`

func newDefinitionClient(t *testing.T, files map[string]string) *apiclient.Client {
	t.Helper()
	dir := t.TempDir()
	globalDir := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(globalDir, name+".yaml"), []byte(src), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := workflow.NewRegistry(globalDir, workflow.Options{KnownAgents: []string{"claude"}}, nil)
	reg.ReloadGlobal()
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      logger,
		Store:       st,
		Git:         gitx.New(),
		Worktrees:   worktree.NewManager(gitx.New(), dir),
		Workflows:   reg,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return apiclient.New(ts.URL, testToken)
}

func TestGetWorkflowDefinitionOverTheWire(t *testing.T) {
	c := newDefinitionClient(t, map[string]string{"shipit": laneYAML})

	got, err := c.GetWorkflowDefinition(t.Context(), 0, "shipit")
	if err != nil {
		t.Fatalf("GetWorkflowDefinition: %v", err)
	}
	if !got.Valid() {
		t.Fatalf("entry invalid: %+v", got.Errors)
	}
	if got.Name != "shipit" || got.Scope != "global" || got.File == "" {
		t.Errorf("entry = %+v, want the global shipit with its file", got)
	}
	body := got.Definition
	if body == nil {
		t.Fatal("definition is nil for a valid workflow")
	}
	if body.Defaults.Agent != "claude" || body.Defaults.Model != "sonnet" {
		t.Errorf("defaults = %+v, want the authored block", body.Defaults)
	}
	if len(body.Fields) != 1 || body.Fields[0].Name != "environment" ||
		body.Fields[0].DisplayLabel() != "Environment" || !body.Fields[0].Required {
		t.Errorf("fields = %+v, want the authored input contract", body.Fields)
	}
	if len(body.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(body.Steps))
	}

	plan := body.Steps[0]
	if plan.Check != "go build ./..." {
		t.Errorf("check = %q, want the authored check", plan.Check)
	}
	// The label a graph prints is derived client-side, because the DTO keeps
	// "no name declared" distinct from "named after its id".
	if plan.Name != "" {
		t.Errorf("name = %q, want empty — the file declares none", plan.Name)
	}
	if plan.DisplayName() != "plan" {
		t.Errorf("DisplayName = %q, want the id as the fallback label", plan.DisplayName())
	}

	spread := body.Steps[1]
	if len(spread.Lanes) != 2 || spread.Lanes[0].ID != "api" || spread.Lanes[1].Workflow != "other" {
		t.Fatalf("lanes = %+v, want an inline lane then a reference", spread.Lanes)
	}
	if len(spread.Lanes[1].Steps) != 0 {
		t.Errorf("referenced lane = %+v, want it collapsed rather than expanded", spread.Lanes[1].Steps)
	}
	if spread.Merge == nil || spread.Merge.ConflictPolicy() != "agent" || spread.Merge.Agent == nil {
		t.Fatalf("merge = %+v, want the agent policy and its nested step", spread.Merge)
	}

	repeat := body.Steps[2]
	if len(repeat.ForEach) != 1 || repeat.Count != nil {
		t.Errorf("loop driver = for_each %v / count %v, want the scalar for_each alone",
			repeat.ForEach, repeat.Count)
	}
	if len(repeat.Steps) != 1 || repeat.Steps[0].ID != "body" {
		t.Errorf("loop body = %+v, want the one body step", repeat.Steps)
	}
}

// A broken workflow is a successful response the client must not mistake for
// a transport failure — the findings are the payload.
func TestGetWorkflowDefinitionBrokenOverTheWire(t *testing.T) {
	c := newDefinitionClient(t, map[string]string{
		"broken": "name: broken\nsteps:\n  - {id: a, type: nonsense}\n",
	})

	got, err := c.GetWorkflowDefinition(t.Context(), 0, "broken")
	if err != nil {
		t.Fatalf("GetWorkflowDefinition returned an error for a broken file: %v", err)
	}
	if got.Valid() || got.Definition != nil {
		t.Errorf("entry = %+v, want no body", got)
	}
	if len(got.Errors) == 0 {
		t.Error("no findings on a broken workflow")
	}
}

func TestGetWorkflowDefinitionUnknownName(t *testing.T) {
	c := newDefinitionClient(t, nil)

	_, err := c.GetWorkflowDefinition(t.Context(), 0, "nope")
	if err == nil {
		t.Fatal("GetWorkflowDefinition: want an error for an unknown name")
	}
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("err = %v, want a 404 APIError", err)
	}
}

// A merge that declares no policy still means `block` (§7.6). The DTO reports
// it as authored — empty — and the client resolves it, so "the file said
// block" and "the file said nothing" stay distinguishable on the wire.
func TestWorkflowMergeConflictPolicyDefault(t *testing.T) {
	var m apiclient.WorkflowMergeDef
	if got := m.ConflictPolicy(); got != "block" {
		t.Errorf("ConflictPolicy() = %q, want block", got)
	}
}
