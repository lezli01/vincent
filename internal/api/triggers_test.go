package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/trigger"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// The trigger routes (§13.2, task 096) against a real registry, writer and
// manager over a temp config dir, with the manager's handler the server's own
// inner mux — the daemon's wiring (decision 30), minus the daemon.
//
// The manager is never started. Nothing polls on its own, so every cursor,
// ledger row and event a test observes was written by the request it made;
// that is what lets "a dry run writes nothing" be asserted exactly.

type triggerHarness struct {
	*projectHarness
	dir       string
	reg       *trigger.Registry
	cur       atomic.Pointer[config.Config]
	projectID int64

	envMu sync.Mutex
	env   map[string]string

	pubMu     sync.Mutex
	published []string
}

func newTriggerHarness(t *testing.T) *triggerHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfgDir := t.TempDir()
	if _, err := config.EnsureDefaultFile(cfgDir); err != nil {
		t.Fatalf("seed config.yaml: %v", err)
	}
	h := &triggerHarness{dir: filepath.Join(cfgDir, "triggers"), env: map[string]string{}}
	initial := config.Default()
	h.cur.Store(&initial)
	// The store's hook runs after the event row commits: what it sees is what
	// an SSE subscriber would be sent.
	st.SetEventHook(func(e *store.Event) {
		h.pubMu.Lock()
		defer h.pubMu.Unlock()
		h.published = append(h.published, e.Type)
	})

	git := gitx.New()
	wt := worktree.NewManager(git, t.TempDir())
	wfs := workflow.NewRegistry(filepath.Join(t.TempDir(), "workflows"),
		workflow.Options{KnownAgents: []string{"claude"}}, nil)
	wfs.ReloadGlobal()
	h.reg = trigger.NewRegistry(h.dir, nil)
	h.reg.Reload()

	var srv atomic.Pointer[Server]
	mgr := trigger.NewManager(trigger.Deps{
		Store: st,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			srv.Load().Inner().ServeHTTP(w, r)
		}),
		Registry: h.reg,
		Enabled:  func() bool { return h.cur.Load().Triggers.Enabled },
		Getenv: func(k string) string {
			h.envMu.Lock()
			defer h.envMu.Unlock()
			return h.env[k]
		},
	})
	s := New(Deps{
		Token:       testToken,
		Config:      func() config.Config { return *h.cur.Load() },
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		Dirs:        config.Dirs{Config: cfgDir},
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		ApplyConfig: func(next config.Config) {
			next.Listen = h.cur.Load().Listen
			h.cur.Store(&next)
		},
		Store:     st,
		Git:       git,
		Worktrees: wt,
		Workflows: wfs,
		OnProjectsChanged: func() {
			projects, err := st.ListProjects(t.Context())
			if err != nil {
				return
			}
			roots := make(map[int64]string, len(projects))
			for i := range projects {
				roots[projects[i].ID] = projects[i].Path
			}
			wfs.SetProjects(roots)
		},
		Triggers:        mgr,
		TriggerRegistry: h.reg,
		TriggerWriter:   trigger.NewWriter(h.dir),
	})
	srv.Store(s)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	h.projectHarness = &projectHarness{ts: ts, store: st, wt: wt}

	p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})
	id, ok := p["id"].(float64)
	if !ok {
		t.Fatalf("project id = %v", p["id"])
	}
	h.projectID = int64(id)
	return h
}

func (h *triggerHarness) setenv(k, v string) {
	h.envMu.Lock()
	defer h.envMu.Unlock()
	h.env[k] = v
}

// publishedCount counts the post-commit publications of one event type.
func (h *triggerHarness) publishedCount(typ string) int {
	h.pubMu.Lock()
	defer h.pubMu.Unlock()
	n := 0
	for _, p := range h.published {
		if p == typ {
			n++
		}
	}
	return n
}

// writeTrigger puts a file in the directory the way $EDITOR would, and reloads
// the registry the way the watcher would.
func (h *triggerHarness) writeTrigger(t *testing.T, id, src string) {
	t.Helper()
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, id+".yaml"), []byte(src), 0o600); err != nil {
		t.Fatalf("write trigger: %v", err)
	}
	h.reg.Reload()
	if e, ok := h.reg.Get(id); !ok || !e.Valid() {
		t.Fatalf("trigger %s did not load valid: %v", id, e.Errors)
	}
}

func (h *triggerHarness) ledger(t *testing.T, id string) []store.TriggerDelivery {
	t.Helper()
	rows, err := h.store.ListTriggerDeliveries(t.Context(), id, 1000)
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	return rows
}

func (h *triggerHarness) maxEventID(t *testing.T) int64 {
	t.Helper()
	id, err := h.store.MaxEventID(t.Context())
	if err != nil {
		t.Fatalf("max event id: %v", err)
	}
	return id
}

// push posts raw bytes to the ingress, with the bearer token unless bearer is
// false.
func (h *triggerHarness) push(t *testing.T, id string, body []byte, header map[string]string, bearer bool) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.ts.URL+"/v1/triggers/"+id+"/events", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, out
}

func decodeInto(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %T: %v (%s)", v, err, body)
	}
}

// errorDetails asserts the envelope and returns its details.
func errorDetails(t *testing.T, resp *http.Response, body []byte, status int, code string) map[string]string {
	t.Helper()
	wantError(t, resp, body, status, code)
	var e errorBody
	decodeInto(t, body, &e)
	return e.Error.Details
}

// The poll command in these tests is this test binary re-executed, so no test
// depends on a shell — Windows runs the same child (internal/trigger's
// pattern). The sentinel is a bare word: flag parsing stops at it.
const triggerHelperSentinel = "trigger-api-helper"

func triggerHelperArgv(t *testing.T, mode string, lines ...string) []string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return append([]string{self, "-test.run=TestTriggerAPIHelperProcess", triggerHelperSentinel, mode}, lines...)
}

// TestTriggerAPIHelperProcess is not a test: it is a trigger's poll command.
func TestTriggerAPIHelperProcess(t *testing.T) {
	i := slices.Index(os.Args, triggerHelperSentinel)
	if i < 0 {
		t.Skip("not running as a trigger's poll command")
	}
	args := os.Args[i+1:]
	switch args[0] {
	case "print":
		for _, l := range args[1:] {
			fmt.Println(l)
		}
	case "fail":
		fmt.Fprintln(os.Stderr, "helper: deliberate failure")
		os.Exit(3)
	}
	os.Exit(0)
}

// commandTrigger is a disabled command trigger matching `kind: bug`. The argv
// is written as a JSON flow list, which is valid YAML whatever the path holds
// — a Windows backslash, a macOS "Application Support".
func commandTrigger(id string, project int64, argv []string) string {
	cmd, _ := json.Marshal(argv)
	return "# hand-written: this comment must survive every write\n" +
		"id: " + id + "\n" +
		"enabled: false\n" +
		"source:\n" +
		"  type: command\n" +
		"  project: " + strconv.FormatInt(project, 10) + "\n" +
		"  poll_interval: 5m\n" +
		"  command: " + string(cmd) + "\n" +
		"match:\n" +
		"  kind: bug\n" +
		"action:\n" +
		"  type: create_task\n" +
		"  title: \"{{ .Event.title }}\"\n"
}

func httpTrigger(id string, project int64, secretEnv string, enabled bool) string {
	return "id: " + id + "\n" +
		"enabled: " + strconv.FormatBool(enabled) + "\n" +
		"source:\n" +
		"  type: http\n" +
		"  project: " + strconv.FormatInt(project, 10) + "\n" +
		"  signature:\n" +
		"    scheme: " + trigger.SignatureGitHubHMACSHA256 + "\n" +
		"    secret_env: " + secretEnv + "\n" +
		"action:\n" +
		"  type: create_task\n" +
		"  title: \"{{ .Event.title }}\"\n"
}

func TestTriggerCRUDOverTheAPI(t *testing.T) {
	h := newTriggerHarness(t)
	ctx := t.Context()

	// Create: the daemon renders the starter, owner-only.
	resp, body := h.doJSON(t, http.MethodPost, "/v1/triggers", map[string]any{
		"id": "nightly", "project_id": h.projectID, "title": "Nightly {{ .Event.id }}",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d: %s", resp.StatusCode, body)
	}
	var created triggerWriteResponse
	decodeInto(t, body, &created)
	path := filepath.Join(h.dir, "nightly.yaml")
	if created.ID != "nightly" || created.File != path || created.Version == "" {
		t.Errorf("create response = %+v, want nightly at %s with a version", created, path)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	starter := trigger.Starter(trigger.StarterSpec{ID: "nightly", Project: h.projectID, Title: "Nightly {{ .Event.id }}"})
	if !bytes.Equal(src, starter) {
		t.Errorf("created file is not the starter:\n%s", src)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := fi.Mode().Perm(); got != config.FilePerm {
			t.Errorf("created mode = %o, want %o (decision 20)", got, config.FilePerm)
		}
	}

	resp, body = h.doJSON(t, http.MethodPost, "/v1/triggers", map[string]any{"id": "nightly", "project_id": h.projectID})
	wantError(t, resp, body, http.StatusConflict, CodeInvalidState)
	resp, body = h.doJSON(t, http.MethodPost, "/v1/triggers", map[string]any{"id": "orphan", "project_id": h.projectID + 99})
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)
	resp, body = h.doJSON(t, http.MethodPost, "/v1/triggers", map[string]any{"id": "../escape", "project_id": h.projectID})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)

	// List and get.
	resp, body = h.doJSON(t, http.MethodGet, "/v1/triggers", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d: %s", resp.StatusCode, body)
	}
	var list triggerListResponse
	decodeInto(t, body, &list)
	if list.Enabled || list.Dir != h.dir || len(list.Triggers) != 1 {
		t.Fatalf("list = %+v, want the one trigger with triggers.enabled off", list)
	}
	row := list.Triggers[0]
	if row.ID != "nightly" || row.Version != created.Version || !row.Valid || row.Enabled || row.Armed ||
		row.DisarmedReason == "" || row.SourceType != trigger.SourceCommand || row.ActionType != trigger.ActionCreateTask ||
		row.ProjectID != h.projectID || row.OnFire != trigger.OnFirePropose ||
		row.Permission != trigger.PermissionRestricted || row.Poll.Seeded {
		t.Errorf("list row = %+v", row)
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/triggers/nightly", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status %d: %s", resp.StatusCode, body)
	}
	var detail triggerDetail
	decodeInto(t, body, &detail)
	if detail.Source != string(src) || detail.Definition == nil || detail.Definition.Action.Title != "Nightly {{ .Event.id }}" {
		t.Errorf("detail = %+v, want the file's bytes and its definition", detail)
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/triggers/nope", nil)
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)

	// Patch with the version: 200, a new version, the comment kept.
	setTitle := []workflow.Op{{Kind: workflow.OpSet, Path: "action.title", Value: workflow.RenderScalar("Nightly run {{ .Event.id }}")}}
	resp, body = h.doJSON(t, http.MethodPatch, "/v1/triggers/nightly", map[string]any{"version": created.Version, "ops": setTitle})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: status %d: %s", resp.StatusCode, body)
	}
	var patched triggerWriteResponse
	decodeInto(t, body, &patched)
	if patched.Version == "" || patched.Version == created.Version {
		t.Errorf("patched version = %q, want a new token (was %q)", patched.Version, created.Version)
	}
	src, _ = os.ReadFile(path)
	if !bytes.Contains(src, []byte("Nightly run")) || !bytes.HasPrefix(src, []byte("# vincent trigger:")) {
		t.Errorf("patched file lost the edit or its header comment:\n%s", src)
	}
	if e, _ := h.reg.Get("nightly"); e.Version != patched.Version {
		t.Errorf("registry version = %q, want %q: the answer must follow a reload", e.Version, patched.Version)
	}

	// A stale version is a 409 carrying the current one; the file stays.
	resp, body = h.doJSON(t, http.MethodPatch, "/v1/triggers/nightly", map[string]any{"version": created.Version, "ops": setTitle})
	if d := errorDetails(t, resp, body, http.StatusConflict, CodeInvalidState); d["version"] != patched.Version {
		t.Errorf("stale 409 details = %v, want version %q", d, patched.Version)
	}

	// A patch whose result does not validate is refused byte-identical.
	before, _ := os.ReadFile(path)
	resp, body = h.doJSON(t, http.MethodPatch, "/v1/triggers/nightly", map[string]any{
		"version": patched.Version,
		"ops":     []workflow.Op{{Kind: workflow.OpSet, Path: "source.poll_interval", Value: "1ms"}},
	})
	if d := errorDetails(t, resp, body, http.StatusBadRequest, CodeValidationFailed); !strings.Contains(d["errors"], "source.poll_interval") {
		t.Errorf("invalid patch details = %v, want the poll_interval finding", d)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("a refused patch changed the file:\n%s", after)
	}
	resp, body = h.doJSON(t, http.MethodPatch, "/v1/triggers/nightly", map[string]any{"ops": setTitle})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	resp, body = h.doJSON(t, http.MethodPatch, "/v1/triggers/nightly", map[string]any{"version": patched.Version, "ops": []workflow.Op{}})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)

	// Delete keeps the ledger (decision 21) and drops the cursor (decision 16).
	older, newer := time.Now().Add(-time.Hour).UTC(), time.Now().UTC()
	for _, d := range []*store.TriggerDelivery{
		{TriggerID: "nightly", EventID: "e1", DedupeKey: "e1", Outcome: store.DeliveryFired, CreatedAt: older},
		{TriggerID: "nightly", EventID: "e2", DedupeKey: "e2", Outcome: store.DeliveryFiltered, CreatedAt: newer},
	} {
		if _, err := h.store.RecordTriggerDelivery(ctx, d); err != nil {
			t.Fatalf("record delivery: %v", err)
		}
	}
	mark := "w1"
	if err := h.store.PutTriggerCursor(ctx, &store.TriggerCursor{TriggerID: "nightly", Cursor: &mark, LastPollAt: &newer, LastPollOK: true}); err != nil {
		t.Fatalf("put cursor: %v", err)
	}

	resp, body = h.doJSON(t, http.MethodDelete, "/v1/triggers/nightly", nil)
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	resp, body = h.doJSON(t, http.MethodDelete, "/v1/triggers/nightly?version="+created.Version, nil)
	if d := errorDetails(t, resp, body, http.StatusConflict, CodeInvalidState); d["version"] != patched.Version {
		t.Errorf("stale delete details = %v, want version %q", d, patched.Version)
	}
	resp, body = h.doJSON(t, http.MethodDelete, "/v1/triggers/nightly?version="+patched.Version, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file after delete: %v, want it gone", err)
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/triggers/nightly", nil)
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)
	if _, err := h.store.GetTriggerCursor(ctx, "nightly"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cursor after delete: %v, want it dropped", err)
	}

	// The ledger outlives the file, newest first.
	resp, body = h.doJSON(t, http.MethodGet, "/v1/triggers/nightly/deliveries", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deliveries: status %d: %s", resp.StatusCode, body)
	}
	var ledger triggerDeliveriesResponse
	decodeInto(t, body, &ledger)
	if len(ledger.Deliveries) != 2 || ledger.Deliveries[0].EventID != "e2" || ledger.Deliveries[1].EventID != "e1" {
		t.Fatalf("deliveries = %+v, want e2 then e1", ledger.Deliveries)
	}
	if d := ledger.Deliveries[1]; d.TriggerID != "nightly" || d.Outcome != store.DeliveryFired || d.TaskID != nil || d.CreatedAt == "" {
		t.Errorf("delivery row = %+v", d)
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/triggers/nightly/deliveries?limit=1", nil)
	decodeInto(t, body, &ledger)
	if resp.StatusCode != http.StatusOK || len(ledger.Deliveries) != 1 || ledger.Deliveries[0].EventID != "e2" {
		t.Errorf("limit=1: status %d, deliveries %+v, want only e2", resp.StatusCode, ledger.Deliveries)
	}
	resp, body = h.doJSON(t, http.MethodGet, "/v1/triggers/nightly/deliveries?limit=0", nil)
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
}

// TestTriggerSchemaServesTheDescriptor is the served schema's drift test: the
// route serves exactly what the validator's constants build.
func TestTriggerSchemaServesTheDescriptor(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/triggers/schema", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	want, err := json.Marshal(trigger.SchemaDescriptor())
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	var got, wantAny any
	decodeInto(t, body, &got)
	decodeInto(t, want, &wantAny)
	if !reflect.DeepEqual(got, wantAny) {
		t.Errorf("served schema differs from trigger.SchemaDescriptor():\n got %s\nwant %s", body, want)
	}
}

// TestTriggerLiteralSiblingsAnswer405: `schema` and `validate` are literal
// siblings of the `{id}` wildcard, so a wrong method on them must be the §13.1
// 405 with its Allow header — not a fall-through into the `{id}` routes.
func TestTriggerLiteralSiblingsAnswer405(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	for _, c := range []struct{ method, path, allow string }{
		{http.MethodPost, "/v1/triggers/schema", http.MethodGet},
		{http.MethodGet, "/v1/triggers/validate", http.MethodPost},
	} {
		resp, body := doRequest(t, ts, c.method, c.path, testToken)
		wantError(t, resp, body, http.StatusMethodNotAllowed, CodeMethodNotAllowed)
		if allow := resp.Header.Get("Allow"); allow != c.allow {
			t.Errorf("%s %s: Allow = %q, want %q", c.method, c.path, allow, c.allow)
		}
	}
}

func TestTriggerValidateRoute(t *testing.T) {
	h := newTriggerHarness(t)
	valid := commandTrigger("probe", h.projectID, []string{"poll"})
	for _, c := range []struct {
		name, source, id string
		valid            bool
		path             string
	}{
		{name: "valid, no file", source: valid, valid: true},
		{name: "valid for its stem", source: valid, id: "probe", valid: true},
		{name: "id not the stem", source: valid, id: "other", path: "id"},
		{name: "interval under the floor", source: strings.Replace(valid, "5m", "1ms", 1), path: "source.poll_interval"},
		{name: "not YAML", source: "id: [", path: ""},
	} {
		resp, body := h.doJSON(t, http.MethodPost, "/v1/triggers/validate", map[string]any{"source": c.source, "id": c.id})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d: %s", c.name, resp.StatusCode, body)
		}
		var got triggerValidateResponse
		decodeInto(t, body, &got)
		if got.Valid != c.valid || got.Errors == nil || (c.valid != (len(got.Errors) == 0)) {
			t.Errorf("%s: response = %+v, want valid=%v", c.name, got, c.valid)
			continue
		}
		if c.path != "" && !slices.ContainsFunc(got.Errors, func(e workflow.Error) bool { return e.Path == c.path }) {
			t.Errorf("%s: errors = %+v, want one at %s", c.name, got.Errors, c.path)
		}
	}
}

// cursorText flattens a cursor row for an exact before/after comparison.
func cursorText(t *testing.T, h *triggerHarness, id string) string {
	t.Helper()
	c, err := h.store.GetTriggerCursor(t.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return "<none>"
	}
	if err != nil {
		t.Fatalf("get cursor: %v", err)
	}
	ts := func(p *time.Time) string {
		if p == nil {
			return "nil"
		}
		return p.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("cursor=%v last_poll=%s ok=%v err=%q last_fire=%s",
		deref(c.Cursor), ts(c.LastPollAt), c.LastPollOK, c.LastPollError, ts(c.LastFireAt))
}

func deref(s *string) string {
	if s == nil {
		return "nil"
	}
	return *s
}

// TestTriggerDryRunsWriteNothing: both dry runs (#362) go through the real
// pipeline and leave the cursor, its poll health, the ledger and the events
// table exactly as they were — on a trigger that is disabled with
// triggers.enabled off, because they fire nothing.
func TestTriggerDryRunsWriteNothing(t *testing.T) {
	h := newTriggerHarness(t)
	ctx := t.Context()
	h.writeTrigger(t, "poller", commandTrigger("poller", h.projectID, triggerHelperArgv(t, "print",
		`{"id":"e1","kind":"bug","title":"First"}`,
		`{"id":"e2","kind":"chore","title":"Second"}`,
		`{"cursor":"w2"}`,
	)))
	mark, polled := "w1", time.Now().Add(-time.Minute).UTC()
	if err := h.store.PutTriggerCursor(ctx, &store.TriggerCursor{
		TriggerID: "poller", Cursor: &mark, LastPollAt: &polled, LastPollError: "an earlier failure",
	}); err != nil {
		t.Fatalf("put cursor: %v", err)
	}
	if _, err := h.store.RecordTriggerDelivery(ctx, &store.TriggerDelivery{
		TriggerID: "poller", EventID: "old", DedupeKey: "old", Outcome: store.DeliveryFired, CreatedAt: polled,
	}); err != nil {
		t.Fatalf("record delivery: %v", err)
	}
	h.writeTrigger(t, "broken-poll", commandTrigger("broken-poll", h.projectID, triggerHelperArgv(t, "fail")))
	h.writeTrigger(t, "hook", httpTrigger("hook", h.projectID, "HOOK_SECRET", true))

	cursorBefore, ledgerBefore, eventsBefore := cursorText(t, h, "poller"), h.ledger(t, "poller"), h.maxEventID(t)

	test := func(event map[string]any) trigger.Judgement {
		t.Helper()
		resp, body := h.doJSON(t, http.MethodPost, "/v1/triggers/poller/test", map[string]any{"event": event})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("test: status %d: %s", resp.StatusCode, body)
		}
		var j trigger.Judgement
		decodeInto(t, body, &j)
		return j
	}
	j := test(map[string]any{"id": "new", "kind": "bug", "title": "Crash on start"})
	if !j.Matched || j.Outcome != store.DeliveryFired || j.DedupeKey != "new" || j.WouldDedupe || j.Action == nil ||
		j.Action.Method != http.MethodPost || j.Action.Path != "/v1/tasks" {
		t.Fatalf("matching judgement = %+v", j)
	}
	if b, _ := j.Action.Body.(map[string]any); b["title"] != "Crash on start" || b["paused"] != true || b["restricted"] != true {
		t.Errorf("rendered body = %v, want the title, paused (propose) and restricted", j.Action.Body)
	}
	if j := test(map[string]any{"id": "old", "kind": "bug", "title": "again"}); !j.WouldDedupe || j.Outcome != store.DeliveryDeduped {
		t.Errorf("delivered key judgement = %+v, want would_dedupe", j)
	}
	if j := test(map[string]any{"id": "n2", "kind": "chore"}); j.Matched || j.MatchMiss != "kind" || j.Outcome != store.DeliveryFiltered || j.Action != nil {
		t.Errorf("filtered judgement = %+v, want a miss on kind", j)
	}
	resp, body := h.doJSON(t, http.MethodPost, "/v1/triggers/poller/test", map[string]any{})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	resp, body = h.doJSON(t, http.MethodPost, "/v1/triggers/nope/test", map[string]any{"event": map[string]any{"id": "x"}})
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)

	// The live poll runs the command for real and judges what it printed.
	resp, body = h.doJSON(t, http.MethodPost, "/v1/triggers/poller/poll", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll: status %d: %s", resp.StatusCode, body)
	}
	var dp trigger.DryPoll
	decodeInto(t, body, &dp)
	if dp.Seed || dp.Error != "" || len(dp.Events) != 2 || dp.Cursor == nil || *dp.Cursor != "w2" {
		t.Fatalf("dry poll = %+v, want two judged events and the printed cursor", dp)
	}
	if dp.Events[0].EventID != "e1" || dp.Events[0].Outcome != store.DeliveryFired ||
		dp.Events[1].EventID != "e2" || dp.Events[1].Outcome != store.DeliveryFiltered {
		t.Errorf("dry poll judgements = %+v, %+v", dp.Events[0], dp.Events[1])
	}

	// A failing command is still an answer, and records no poll health.
	resp, body = h.doJSON(t, http.MethodPost, "/v1/triggers/broken-poll/poll", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failing poll: status %d: %s", resp.StatusCode, body)
	}
	decodeInto(t, body, &dp)
	if !strings.Contains(dp.Error, "exit status 3") || len(dp.Events) != 0 {
		t.Errorf("failing dry poll = %+v, want the exit status", dp)
	}
	if got := cursorText(t, h, "broken-poll"); got != "<none>" {
		t.Errorf("failing dry poll wrote a cursor: %s", got)
	}
	resp, body = h.doJSON(t, http.MethodPost, "/v1/triggers/hook/poll", nil)
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)

	if got := cursorText(t, h, "poller"); got != cursorBefore {
		t.Errorf("cursor changed by a dry run:\n got %s\nwant %s", got, cursorBefore)
	}
	if got := h.ledger(t, "poller"); !reflect.DeepEqual(got, ledgerBefore) {
		t.Errorf("ledger changed by a dry run: %+v, was %+v", got, ledgerBefore)
	}
	if got := h.maxEventID(t); got != eventsBefore {
		t.Errorf("events table moved from %d to %d during dry runs", eventsBefore, got)
	}
}

// TestTriggerIngress is 096.5 through the route (decision 31G): bearer and
// HMAC both required, every refusal before the pipeline leaves no ledger row,
// and a delivery fires the real create route and announces itself post-commit.
func TestTriggerIngress(t *testing.T) {
	h := newTriggerHarness(t)
	ctx := t.Context()
	h.setenv("HOOK_SECRET", "s3cret")
	h.writeTrigger(t, "hook", httpTrigger("hook", h.projectID, "HOOK_SECRET", true))
	h.writeTrigger(t, "nosecret", httpTrigger("nosecret", h.projectID, "UNSET_SECRET", true))
	h.writeTrigger(t, "idle", httpTrigger("idle", h.projectID, "HOOK_SECRET", false))
	h.writeTrigger(t, "poller", commandTrigger("poller", h.projectID, []string{"never-run"}))

	body := []byte(`{"title":"From CI","ref":"main"}`)
	signed := func(secret string, b []byte, delivery string) map[string]string {
		hd := map[string]string{trigger.HeaderGitHubSignature: trigger.SignGitHub(secret, b)}
		if delivery != "" {
			hd[trigger.HeaderGitHubDelivery] = delivery
		}
		return hd
	}

	// Off globally: disarmed, with the reason.
	resp, out := h.push(t, "hook", body, signed("s3cret", body, "delivery-1"), true)
	if d := errorDetails(t, resp, out, http.StatusConflict, CodeInvalidState); !strings.Contains(d["reason"], "triggers.enabled") {
		t.Errorf("disarmed details = %v, want the triggers.enabled reason", d)
	}
	resp, out = h.doJSON(t, http.MethodPatch, "/v1/config", map[string]any{"triggers": map[string]any{"enabled": true}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable triggers: status %d: %s", resp.StatusCode, out)
	}

	resp, out = h.push(t, "idle", body, signed("s3cret", body, "delivery-1"), true)
	if d := errorDetails(t, resp, out, http.StatusConflict, CodeInvalidState); !strings.Contains(d["reason"], "disabled") {
		t.Errorf("disabled trigger details = %v, want the disabled reason", d)
	}
	resp, out = h.push(t, "nope", body, signed("s3cret", body, "delivery-1"), true)
	wantError(t, resp, out, http.StatusNotFound, CodeNotFound)
	resp, out = h.push(t, "poller", body, signed("s3cret", body, "delivery-1"), true)
	wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)

	for name, c := range map[string]struct {
		id     string
		header map[string]string
		bearer bool
	}{
		"missing signature": {"hook", map[string]string{trigger.HeaderGitHubDelivery: "delivery-1"}, true},
		"wrong secret":      {"hook", signed("not-the-secret", body, "delivery-1"), true},
		"malformed":         {"hook", map[string]string{trigger.HeaderGitHubSignature: "sha256=zz", trigger.HeaderGitHubDelivery: "delivery-1"}, true},
		"unset secret env":  {"nosecret", signed("s3cret", body, "delivery-1"), true},
		"no bearer":         {"hook", signed("s3cret", body, "delivery-1"), false},
	} {
		resp, out := h.push(t, c.id, body, c.header, c.bearer)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401 (%s)", name, resp.StatusCode, out)
		}
		wantError(t, resp, out, http.StatusUnauthorized, CodeUnauthorized)
	}

	big := bytes.Repeat([]byte("a"), maxLargeRequestBytes+1)
	resp, out = h.push(t, "hook", big, signed("s3cret", big, "delivery-big"), true)
	wantError(t, resp, out, http.StatusRequestEntityTooLarge, CodePayloadTooLarge)
	resp, out = h.push(t, "hook", body, signed("s3cret", body, ""), true)
	wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)

	for _, id := range []string{"hook", "nosecret", "idle", "poller"} {
		if rows := h.ledger(t, id); len(rows) != 0 {
			t.Errorf("%s: refused pushes left ledger rows %+v", id, rows)
		}
	}
	if n := h.publishedCount(store.EventTriggerFired); n != 0 {
		t.Fatalf("trigger.fired published %d times before any delivery", n)
	}

	// A signed push with the delivery header fires, and the id is the header.
	resp, out = h.push(t, "hook", body, signed("s3cret", body, "delivery-1"), true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid push: status %d: %s", resp.StatusCode, out)
	}
	var del trigger.Delivery
	decodeInto(t, out, &del)
	if del.Outcome != store.DeliveryFired || del.EventID != "delivery-1" || del.TaskID == nil || del.DeliveryID == 0 {
		t.Fatalf("delivery = %+v, want fired with a task, keyed by the delivery header", del)
	}
	resp, out = h.doJSON(t, http.MethodGet, "/v1/tasks/"+strconv.FormatInt(*del.TaskID, 10), nil)
	var task map[string]any
	decodeInto(t, out, &task)
	if resp.StatusCode != http.StatusOK || task["state"] != "paused" || task["title"] != "From CI" {
		t.Errorf("created task = %d %v, want the paused (propose) task titled From CI", resp.StatusCode, task)
	}
	rows := h.ledger(t, "hook")
	if len(rows) != 1 || rows[0].ID != del.DeliveryID || rows[0].Outcome != store.DeliveryFired ||
		rows[0].TaskID == nil || *rows[0].TaskID != *del.TaskID {
		t.Errorf("ledger = %+v, want the one fired row for task %d", rows, *del.TaskID)
	}
	fired, err := h.store.ListEvents(ctx, store.EventFilter{Types: []string{store.EventTriggerFired}})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(fired) != 1 || fired[0].ProjectID == nil || *fired[0].ProjectID != h.projectID ||
		fired[0].TaskID == nil || *fired[0].TaskID != *del.TaskID || !bytes.Contains(fired[0].Payload, []byte(`"hook"`)) {
		t.Errorf("trigger.fired rows = %+v, want one for the project and task", fired)
	}
	if n := h.publishedCount(store.EventTriggerFired); n != 1 {
		t.Errorf("trigger.fired published %d times post-commit, want 1", n)
	}

	// A redelivery dedupes: no second task, no second announcement.
	// Each answer decodes into a fresh value: Unmarshal into a used struct
	// keeps the fields an omitempty body leaves out.
	resp, out = h.push(t, "hook", body, signed("s3cret", body, "delivery-1"), true)
	var again trigger.Delivery
	decodeInto(t, out, &again)
	if resp.StatusCode != http.StatusOK || again.Outcome != store.DeliveryDeduped || again.TaskID != nil {
		t.Errorf("redelivery = %d %+v, want deduped", resp.StatusCode, again)
	}
	if rows := h.ledger(t, "hook"); len(rows) != 2 {
		t.Errorf("ledger after redelivery = %+v, want the fired and the deduped row", rows)
	}

	// An id in the payload wins over the header.
	withID := []byte(`{"id":"payload-7","title":"Second"}`)
	resp, out = h.push(t, "hook", withID, signed("s3cret", withID, "delivery-2"), true)
	var second trigger.Delivery
	decodeInto(t, out, &second)
	if resp.StatusCode != http.StatusOK || second.EventID != "payload-7" || second.Outcome != store.DeliveryFired {
		t.Errorf("payload id push = %d %+v, want fired as payload-7", resp.StatusCode, second)
	}
	if n := h.publishedCount(store.EventTriggerFired); n != 2 {
		t.Errorf("trigger.fired published %d times, want 2 (the dedupe announces nothing)", n)
	}
}

// TestTriggerConfigSwitchRoundTrip: triggers.enabled is served on
// GET /v1/config, written by PATCH, applied before the answer, and is the
// list's global-off banner.
func TestTriggerConfigSwitchRoundTrip(t *testing.T) {
	h := newTriggerHarness(t)
	enabled := func() (bool, bool) {
		t.Helper()
		_, body := h.doJSON(t, http.MethodGet, "/v1/config", nil)
		var cfg configResponse
		decodeInto(t, body, &cfg)
		_, body = h.doJSON(t, http.MethodGet, "/v1/triggers", nil)
		var list triggerListResponse
		decodeInto(t, body, &list)
		return cfg.Triggers.Enabled, list.Enabled
	}
	if cfg, list := enabled(); cfg || list {
		t.Fatalf("default triggers.enabled = %v / list %v, want off", cfg, list)
	}
	for _, want := range []bool{true, false} {
		resp, body := h.doJSON(t, http.MethodPatch, "/v1/config", map[string]any{"triggers": map[string]any{"enabled": want}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("patch %v: status %d: %s", want, resp.StatusCode, body)
		}
		var cfg configResponse
		decodeInto(t, body, &cfg)
		if cfg.Triggers.Enabled != want {
			t.Errorf("patch answer triggers.enabled = %v, want %v", cfg.Triggers.Enabled, want)
		}
		if c, l := enabled(); c != want || l != want {
			t.Errorf("after patch %v: config %v, list %v", want, c, l)
		}
	}
	resp, body := h.doJSON(t, http.MethodPatch, "/v1/config", map[string]any{"triggers": map[string]any{"enabled": "yes"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("non-bool triggers.enabled: status %d, want 400 (%s)", resp.StatusCode, body)
	}
}
