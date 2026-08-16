package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// workflowHarness is a project harness whose server also serves a real
// workflow registry over temp directories.
type workflowHarness struct {
	*projectHarness
	reg       *workflow.Registry
	globalDir string
}

func newWorkflowHarness(t *testing.T) *workflowHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	wt := worktree.NewManager(git, t.TempDir())
	globalDir := filepath.Join(t.TempDir(), "workflows")
	reg := workflow.NewRegistry(globalDir, workflow.Options{KnownAgents: []string{"claude"}}, nil)
	s := New(Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Git:         git,
		Worktrees:   wt,
		Workflows:   reg,
		OnProjectsChanged: func() {
			projects, err := st.ListProjects(t.Context())
			if err != nil {
				return
			}
			roots := make(map[int64]string, len(projects))
			for i := range projects {
				roots[projects[i].ID] = projects[i].Path
			}
			reg.SetProjects(roots)
		},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &workflowHarness{
		projectHarness: &projectHarness{ts: ts, store: st, wt: wt},
		reg:            reg,
		globalDir:      globalDir,
	}
}

func writeWorkflowFile(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(src), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
}

func manualYAML(name, marker string) string {
	return "name: " + name + "\ndescription: " + marker +
		"\nsteps:\n  - {id: gate, type: manual, instructions: " + marker + "}\n"
}

type workflowEntryBody struct {
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	ProjectID   *int64 `json:"project_id"`
	File        string `json:"file"`
	Description string `json:"description"`
	Steps       []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Type  string `json:"type"`
		Agent string `json:"agent"`
	} `json:"steps"`
	Platforms         []string `json:"platforms"`
	PlatformSupported bool     `json:"platform_supported"`
	Error             *string  `json:"error"`
}

type workflowListBody struct {
	Workflows []workflowEntryBody `json:"workflows"`
}

func decodeWorkflowList(t *testing.T, body []byte) workflowListBody {
	t.Helper()
	var out workflowListBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("workflow list not JSON: %v (%s)", err, body)
	}
	return out
}

func TestWorkflowListBuiltinOnly(t *testing.T) {
	h := newWorkflowHarness(t)
	resp, body := h.doJSON(t, http.MethodGet, "/v1/workflows", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	list := decodeWorkflowList(t, body)
	if len(list.Workflows) != 1 {
		t.Fatalf("workflows = %d, want the built-in only: %s", len(list.Workflows), body)
	}
	wf := list.Workflows[0]
	if wf.Name != "adhoc" || wf.Scope != "builtin" {
		t.Errorf("entry = %+v, want the built-in adhoc", wf)
	}
	if len(wf.Steps) != 1 || wf.Steps[0].Type != "agent" || wf.Steps[0].Agent != "claude" {
		t.Errorf("steps = %+v, want one claude agent step", wf.Steps)
	}
}

func TestWorkflowListScopesAndShadowing(t *testing.T) {
	h := newWorkflowHarness(t)
	repo := testrepo.Init(t, "main")
	writeWorkflowFile(t, h.globalDir, "feature", manualYAML("feature", "global"))
	writeWorkflowFile(t, filepath.Join(repo, workflow.ProjectDirName), "feature", manualYAML("feature", "project"))
	h.reg.ReloadGlobal()

	p := h.mustCreate(t, map[string]any{"path": repo})
	id := int64(p["id"].(float64))

	// Without project_id: global scope wins.
	_, body := h.doJSON(t, http.MethodGet, "/v1/workflows", nil)
	feature := findWorkflow(t, decodeWorkflowList(t, body), "feature")
	if feature.Scope != "global" || feature.Description != "global" {
		t.Errorf("unscoped feature = %+v, want the global copy", feature)
	}

	// With project_id: the project copy shadows it (registration wired the
	// project scope through OnProjectsChanged).
	_, body = h.doJSON(t, http.MethodGet, "/v1/workflows?project_id="+strconv.FormatInt(id, 10), nil)
	feature = findWorkflow(t, decodeWorkflowList(t, body), "feature")
	if feature.Scope != "project" || feature.Description != "project" {
		t.Errorf("scoped feature = %+v, want the project copy", feature)
	}
	if feature.ProjectID == nil || *feature.ProjectID != id {
		t.Errorf("project_id = %v, want %d", feature.ProjectID, id)
	}
	if feature.File == "" {
		t.Error("project entry has no file path")
	}
}

func TestWorkflowListShowsBrokenEntry(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "good", manualYAML("good", "ok"))
	writeWorkflowFile(t, h.globalDir, "broken", "name: broken\nsteps:\n  - {id: a, type: nonsense}\n")
	h.reg.ReloadGlobal()

	_, body := h.doJSON(t, http.MethodGet, "/v1/workflows", nil)
	list := decodeWorkflowList(t, body)
	good := findWorkflow(t, list, "good")
	if good.Error != nil {
		t.Errorf("good workflow carries an error: %v", *good.Error)
	}
	broken := findWorkflow(t, list, "broken")
	if broken.Error == nil {
		t.Fatal("broken workflow has no error field")
	}
}

// foreignPlatform names a platform this test process is not running on, so
// the "restricted elsewhere" cases mean the same thing on all three CI legs.
func foreignPlatform() string {
	if runtime.GOOS == workflow.PlatformWindows {
		return workflow.PlatformLinux
	}
	return workflow.PlatformWindows
}

// TestWorkflowListPlatformRestriction covers the §8.1 restriction as the
// registry reports it: a workflow this host cannot run stays listed (it is
// not broken, just not here) and says so, and one that names this host is
// indistinguishable from an unrestricted entry (task 010).
func TestWorkflowListPlatformRestriction(t *testing.T) {
	h := newWorkflowHarness(t)
	writeWorkflowFile(t, h.globalDir, "elsewhere",
		"name: elsewhere\nplatforms: ["+foreignPlatform()+"]\n"+
			"steps:\n  - {id: gate, type: manual, instructions: hi}\n")
	writeWorkflowFile(t, h.globalDir, "here",
		"name: here\nplatforms: ["+runtime.GOOS+"]\n"+
			"steps:\n  - {id: gate, type: manual, instructions: hi}\n")
	h.reg.ReloadGlobal()

	list := decodeWorkflowList(t, mustGet(t, h, "/v1/workflows"))

	elsewhere := findWorkflow(t, list, "elsewhere")
	if elsewhere.Error != nil {
		t.Errorf("a platform-restricted workflow is not invalid: %v", *elsewhere.Error)
	}
	if elsewhere.PlatformSupported {
		t.Errorf("entry restricted to %q claims to run on %q", foreignPlatform(), runtime.GOOS)
	}
	if len(elsewhere.Platforms) != 1 || elsewhere.Platforms[0] != foreignPlatform() {
		t.Errorf("platforms = %v, want [%s]", elsewhere.Platforms, foreignPlatform())
	}

	here := findWorkflow(t, list, "here")
	if !here.PlatformSupported {
		t.Errorf("entry restricted to %q does not run on it", runtime.GOOS)
	}

	adhoc := findWorkflow(t, list, "adhoc")
	if !adhoc.PlatformSupported || len(adhoc.Platforms) != 0 {
		t.Errorf("unrestricted built-in = %+v, want no platforms and supported", adhoc)
	}
}

// TestTaskCreateRejectsForeignPlatform is the enforcement half: the entry is
// listed, but a task naming it is refused here (§8.1, task 010).
func TestTaskCreateRejectsForeignPlatform(t *testing.T) {
	h := newWorkflowHarness(t)
	repo := testrepo.Init(t, "main")
	writeWorkflowFile(t, h.globalDir, "elsewhere",
		"name: elsewhere\nplatforms: ["+foreignPlatform()+"]\n"+
			"steps:\n  - {id: gate, type: manual, instructions: hi}\n")
	h.reg.ReloadGlobal()
	p := h.mustCreate(t, map[string]any{"path": repo})
	id := int64(p["id"].(float64))

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "no", "workflow": "elsewhere",
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	for _, want := range []string{"cannot run here", foreignPlatform(), runtime.GOOS} {
		if !strings.Contains(string(body), want) {
			t.Errorf("message %s missing %q", body, want)
		}
	}

	// The unrestricted built-in still creates, so the check refuses the one
	// workflow and not the endpoint.
	resp, body = h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "yes", "workflow": workflow.AdhocName,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("adhoc task: %d %s", resp.StatusCode, body)
	}
}

func mustGet(t *testing.T, h *workflowHarness, path string) []byte {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, path, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
	}
	return body
}

func TestWorkflowListBadProjectID(t *testing.T) {
	h := newWorkflowHarness(t)
	resp, body := h.doJSON(t, http.MethodGet, "/v1/workflows?project_id=abc", nil)
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)

	resp, body = h.doJSON(t, http.MethodGet, "/v1/workflows?project_id=9999", nil)
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)
}

func TestWorkflowValidate(t *testing.T) {
	h := newWorkflowHarness(t)

	resp, body := h.doJSON(t, http.MethodPost, "/v1/workflows/validate",
		map[string]any{"yaml": manualYAML("ok", "fine")})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	var okResp struct {
		Valid  bool             `json:"valid"`
		Name   string           `json:"name"`
		Errors []workflow.Error `json:"errors"`
	}
	if err := json.Unmarshal(body, &okResp); err != nil {
		t.Fatalf("validate body not JSON: %v (%s)", err, body)
	}
	if !okResp.Valid || okResp.Name != "ok" || len(okResp.Errors) != 0 {
		t.Errorf("valid response = %+v, want valid with no errors", okResp)
	}

	// An invalid workflow is a 200 with errors, not an HTTP error.
	resp, body = h.doJSON(t, http.MethodPost, "/v1/workflows/validate",
		map[string]any{"yaml": "name: bad\nsteps:\n  - id: a\n    type: agent\n"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	var badResp struct {
		Valid  bool             `json:"valid"`
		Errors []workflow.Error `json:"errors"`
	}
	if err := json.Unmarshal(body, &badResp); err != nil {
		t.Fatalf("validate body not JSON: %v (%s)", err, body)
	}
	if badResp.Valid || len(badResp.Errors) == 0 {
		t.Fatalf("invalid response = %+v, want valid=false with errors", badResp)
	}
	if badResp.Errors[0].Line == 0 || badResp.Errors[0].Message == "" {
		t.Errorf("error = %+v, want a located message", badResp.Errors[0])
	}

	// The registry's own agent set applies: an unknown agent is rejected.
	_, body = h.doJSON(t, http.MethodPost, "/v1/workflows/validate", map[string]any{
		"yaml": "name: bad\nsteps:\n  - {id: a, type: agent, prompt: hi, agent: gemini}\n",
	})
	if err := json.Unmarshal(body, &badResp); err != nil {
		t.Fatalf("validate body not JSON: %v (%s)", err, body)
	}
	if badResp.Valid {
		t.Error("workflow naming an unregistered agent validated")
	}
}

func TestWorkflowValidateRequiresYAML(t *testing.T) {
	h := newWorkflowHarness(t)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/workflows/validate", map[string]any{})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)

	resp, body = h.doJSON(t, http.MethodPost, "/v1/workflows/validate", map[string]any{"yml": "x"})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
}

func TestWorkflowEndpointsRequireAuth(t *testing.T) {
	h := newWorkflowHarness(t)
	for _, path := range []string{"/v1/workflows", "/v1/workflows/validate"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.ts.URL+path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := h.ts.Client().Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without token: status = %d, want 401 (%s)", path, resp.StatusCode, body)
		}
	}
}

func findWorkflow(t *testing.T, list workflowListBody, name string) workflowEntryBody {
	t.Helper()
	for _, wf := range list.Workflows {
		if wf.Name == name {
			return wf
		}
	}
	t.Fatalf("workflow %q not listed: %+v", name, list.Workflows)
	return workflowEntryBody{}
}
