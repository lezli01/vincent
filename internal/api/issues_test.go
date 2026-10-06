package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// issueHarness is a server over a real store with two projects, for the
// issue routes (task 130.3).
type issueHarness struct {
	srv          *Server
	ts           *httptest.Server
	st           *store.Store
	pid, otherID int64
}

func newIssueHarness(t *testing.T) *issueHarness {
	t.Helper()
	st, err := storetest.Open(filepath.Join(t.TempDir(), "issues.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &store.Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main"}
	other := &store.Project{Name: "other", Path: t.TempDir(), DefaultBranch: "main"}
	for _, pr := range []*store.Project{p, other} {
		if err := st.CreateProject(t.Context(), pr); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
	}
	srv := New(Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &issueHarness{srv: srv, ts: ts, st: st, pid: p.ID, otherID: other.ID}
}

// do sends an authenticated request; body is marshalled unless it is a
// string, which is sent verbatim.
func (h *issueHarness) do(t *testing.T, method, path string, body any, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		enc, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rd = bytes.NewReader(enc)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, rd)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, out
}

// must sends the request and decodes a response of the wanted status.
func (h *issueHarness) must(t *testing.T, want int, method, path string, body any) issueBody {
	t.Helper()
	resp, out := h.do(t, method, path, body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, out)
	}
	var iss issueBody
	if err := json.Unmarshal(out, &iss); err != nil {
		t.Fatalf("decode issue: %v: %s", err, out)
	}
	return iss
}

func (h *issueHarness) create(t *testing.T, body map[string]any) issueBody {
	t.Helper()
	if _, ok := body["project_id"]; !ok {
		body["project_id"] = h.pid
	}
	return h.must(t, http.StatusCreated, http.MethodPost, "/v1/issues", body)
}

// conflict decodes a 409 envelope with its object-valued details.
func conflict(t *testing.T, resp *http.Response, out []byte) map[string]json.RawMessage {
	t.Helper()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, out)
	}
	var env struct {
		Error struct {
			Code    string                     `json:"code"`
			Details map[string]json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode 409: %v", err)
	}
	if env.Error.Code != CodeInvalidState {
		t.Errorf("409 code = %q, want %q", env.Error.Code, CodeInvalidState)
	}
	return env.Error.Details
}

func detailString(t *testing.T, d map[string]json.RawMessage, key string) string {
	t.Helper()
	var s string
	if raw, ok := d[key]; ok {
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("details.%s: %v", key, err)
		}
	}
	return s
}

// issueEvents reads the durable issue.* events after id.
func (h *issueHarness) issueEvents(t *testing.T, after int64) []store.Event {
	t.Helper()
	evs, err := h.st.ListEvents(t.Context(), store.EventFilter{AfterID: after})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return slices.DeleteFunc(evs, func(e store.Event) bool { return !strings.HasPrefix(e.Type, "issue.") })
}

func (h *issueHarness) maxEvent(t *testing.T) int64 {
	t.Helper()
	id, err := h.st.MaxEventID(t.Context())
	if err != nil {
		t.Fatalf("MaxEventID: %v", err)
	}
	return id
}

func eventTypes(evs []store.Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func TestIssueLifecycleOverTheAPI(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)

	iss := h.create(t, map[string]any{"title": "Crash on start", "body": "stack…", "labels": []string{"bug"}, "kind": "bug", "priority": 2})
	if iss.State != "open" || iss.Version != 1 || iss.Body != "stack…" || iss.Kind != "bug" || iss.Priority != 2 {
		t.Fatalf("created = %+v", iss)
	}
	if iss.Author != osUsername() || iss.Author == "" || iss.CreatedByTaskID != nil {
		t.Errorf("author = %q (created_by %v), want the daemon's OS user %q", iss.Author, iss.CreatedByTaskID, osUsername())
	}
	if !slices.Equal(iss.AvailableActions, issuestate.HumanActionsFrom(issuestate.Open)) {
		t.Errorf("available_actions = %v", iss.AvailableActions)
	}
	if iss.Source != nil || len(iss.Editable) != 5 || iss.Tasks.Count != 0 || iss.Tasks.ActiveIDs == nil {
		t.Errorf("source %v editable %v tasks %+v, want a local issue with no tasks", iss.Source, iss.Editable, iss.Tasks)
	}

	got := h.must(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/v1/issues/%d", iss.ID), nil)
	if got.Title != "Crash on start" || !slices.Equal(got.Labels, []string{"bug"}) {
		t.Errorf("get = %+v", got)
	}

	// Fields and labels in one PATCH: one version bump, two events.
	mark := h.maxEvent(t)
	patched := h.must(t, http.StatusOK, http.MethodPatch, fmt.Sprintf("/v1/issues/%d", iss.ID),
		map[string]any{"version": 1, "title": "Crash on cold start", "add_labels": []string{"P1"}, "remove_labels": []string{"BUG"}})
	if patched.Version != 2 || patched.Title != "Crash on cold start" || !slices.Equal(patched.Labels, []string{"P1"}) {
		t.Errorf("patched = version %d title %q labels %v", patched.Version, patched.Title, patched.Labels)
	}
	if got := eventTypes(h.issueEvents(t, mark)); !slices.Equal(got, []string{store.EventIssueUpdated, store.EventIssueLabelsChanged}) {
		t.Errorf("patch events = %v", got)
	}

	// A PATCH that changes nothing writes nothing.
	mark = h.maxEvent(t)
	same := h.must(t, http.StatusOK, http.MethodPatch, fmt.Sprintf("/v1/issues/%d", iss.ID),
		map[string]any{"version": 2, "title": "Crash on cold start", "labels": []string{"p1"}})
	if same.Version != 2 {
		t.Errorf("no-op patch bumped version to %d", same.Version)
	}
	if evs := h.issueEvents(t, mark); len(evs) != 0 {
		t.Errorf("no-op patch appended %v", eventTypes(evs))
	}

	closed := h.must(t, http.StatusOK, http.MethodPost, fmt.Sprintf("/v1/issues/%d/close", iss.ID),
		map[string]any{"reason": "not_planned"})
	if closed.State != "closed" || closed.CloseReason != "not_planned" || closed.ClosedAt == nil ||
		!slices.Equal(closed.AvailableActions, []issuestate.Action{issuestate.Reopen}) {
		t.Errorf("closed = %+v", closed)
	}
	resp, out := h.do(t, http.MethodPost, fmt.Sprintf("/v1/issues/%d/close", iss.ID), nil)
	if d := conflict(t, resp, out); detailString(t, d, "state") != "closed" {
		t.Errorf("second close details = %s", out)
	}
	reopened := h.must(t, http.StatusOK, http.MethodPost, fmt.Sprintf("/v1/issues/%d/reopen", iss.ID), map[string]any{})
	if reopened.State != "open" || reopened.CloseReason != "" {
		t.Errorf("reopened = %+v", reopened)
	}
	resp, out = h.do(t, http.MethodPost, fmt.Sprintf("/v1/issues/%d/reopen", iss.ID), nil)
	if d := conflict(t, resp, out); detailString(t, d, "state") != "open" {
		t.Errorf("second reopen details = %s", out)
	}

	resp, out = h.do(t, http.MethodDelete, fmt.Sprintf("/v1/issues/%d", iss.ID), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", resp.StatusCode, out)
	}
	resp, out = h.do(t, http.MethodGet, fmt.Sprintf("/v1/issues/%d", iss.ID), nil)
	wantError(t, resp, out, http.StatusNotFound, CodeNotFound)

	// No issue.* payload ever carries a body.
	for _, e := range h.issueEvents(t, 0) {
		if strings.Contains(string(e.Payload), "stack") || strings.Contains(string(e.Payload), `"body"`) {
			t.Errorf("%s payload carries the body: %s", e.Type, e.Payload)
		}
	}
}

func TestIssueCreateRefusals(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	cases := []struct {
		name   string
		body   any
		status int
		code   string
	}{
		{"empty title", map[string]any{"project_id": h.pid, "title": "  "}, 400, CodeValidationFailed},
		{"title over 1 KiB", map[string]any{"project_id": h.pid, "title": strings.Repeat("t", maxTitleBytes+1)}, 400, CodeValidationFailed},
		{"body over 64 KiB", map[string]any{"project_id": h.pid, "title": "t", "body": strings.Repeat("b", maxDescriptionBytes+1)}, 400, CodeValidationFailed},
		{"bad kind", map[string]any{"project_id": h.pid, "title": "t", "kind": "Not A Token"}, 400, CodeValidationFailed},
		{"bad priority", map[string]any{"project_id": h.pid, "title": "t", "priority": 9}, 400, CodeValidationFailed},
		{"author is not a field", map[string]any{"project_id": h.pid, "title": "t", "author": "mallory"}, 400, CodeValidationFailed},
		{"no project", map[string]any{"title": "t"}, 400, CodeValidationFailed},
		{"unknown project", map[string]any{"project_id": 999, "title": "t"}, 404, CodeNotFound},
	}
	for _, tc := range cases {
		resp, out := h.do(t, http.MethodPost, "/v1/issues", tc.body)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d: %s", tc.name, resp.StatusCode, tc.status, out)
			continue
		}
		wantError(t, resp, out, tc.status, tc.code)
	}

	// A body at the bound whose JSON escaping doubles it still fits the
	// large tier.
	body := strings.Repeat(`"`, maxDescriptionBytes)
	iss := h.create(t, map[string]any{"title": "quotes", "body": body})
	if len(iss.Body) != maxDescriptionBytes {
		t.Errorf("body length = %d, want %d", len(iss.Body), maxDescriptionBytes)
	}
}

func TestIssuePatchRefusals(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	iss := h.create(t, map[string]any{"title": "t"})
	path := fmt.Sprintf("/v1/issues/%d", iss.ID)

	for name, body := range map[string]any{
		"no version":           map[string]any{"title": "x"},
		"empty patch":          map[string]any{"version": 1},
		"labels and add":       map[string]any{"version": 1, "labels": []string{"a"}, "add_labels": []string{"b"}},
		"labels and remove":    map[string]any{"version": 1, "labels": []string{"a"}, "remove_labels": []string{"b"}},
		"state is not a field": map[string]any{"version": 1, "state": "closed"},
		"title over 1 KiB":     map[string]any{"version": 1, "title": strings.Repeat("t", maxTitleBytes+1)},
	} {
		resp, out := h.do(t, http.MethodPatch, path, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", name, resp.StatusCode, out)
		}
	}

	h.must(t, http.StatusOK, http.MethodPatch, path, map[string]any{"version": 1, "kind": "bug"})
	resp, out := h.do(t, http.MethodPatch, path, map[string]any{"version": 1, "title": "stale"})
	d := conflict(t, resp, out)
	if detailString(t, d, "reason") != issueReasonChanged {
		t.Errorf("stale reason = %s", out)
	}
	var cur issueBody
	if err := json.Unmarshal(d["issue"], &cur); err != nil || cur.Version != 2 || cur.Kind != "bug" {
		t.Errorf("details.issue = %s (%v), want the issue at version 2", d["issue"], err)
	}
	if got := h.must(t, http.StatusOK, http.MethodGet, path, nil); got.Title != "t" {
		t.Errorf("a stale patch wrote title %q", got.Title)
	}
}

func TestIssueMirroredContentIsRefused(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	imported, _, err := h.st.UpsertRemoteIssue(t.Context(), store.RemoteIssue{
		ProjectID: h.pid, Provider: "github", RemoteKey: "I_1", Repo: "o/r", Number: 7,
		URL: "https://github.com/o/r/issues/7", RemoteJSON: `{"state":"OPEN"}`,
		Title: "From GitHub", Body: "upstream", State: issuestate.Open, Labels: []string{"bug"},
	}, issuestate.Sync)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	path := fmt.Sprintf("/v1/issues/%d", imported.ID)
	got := h.must(t, http.StatusOK, http.MethodGet, path, nil)
	if got.Source == nil || got.Source.Provider != "github" || got.Source.Number != 7 || got.Source.RemoteState != "open" {
		t.Errorf("source = %+v", got.Source)
	}
	if !slices.Equal(got.Editable, []string{"kind", "priority"}) {
		t.Errorf("editable = %v", got.Editable)
	}
	for name, body := range map[string]any{
		"title":      map[string]any{"version": got.Version, "title": "mine"},
		"body":       map[string]any{"version": got.Version, "body": "mine"},
		"labels":     map[string]any{"version": got.Version, "labels": []string{}},
		"add_labels": map[string]any{"version": got.Version, "add_labels": []string{"x"}},
	} {
		resp, out := h.do(t, http.MethodPatch, path, body)
		if d := conflict(t, resp, out); detailString(t, d, "reason") != issueReasonMirrored {
			t.Errorf("%s: %s", name, out)
		}
	}
	ok := h.must(t, http.StatusOK, http.MethodPatch, path, map[string]any{"version": got.Version, "kind": "bug", "priority": 1})
	if ok.Kind != "bug" || ok.Priority != 1 {
		t.Errorf("kind/priority patch = %+v", ok)
	}
}

func TestIssueCloseDuplicateOf(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	a := h.create(t, map[string]any{"title": "a"})
	b := h.create(t, map[string]any{"title": "b"})
	elsewhere := h.create(t, map[string]any{"title": "c", "project_id": h.otherID})
	path := fmt.Sprintf("/v1/issues/%d/close", a.ID)

	for name, body := range map[string]any{
		"wrong reason":  map[string]any{"reason": "completed", "duplicate_of": b.ID},
		"no reason":     map[string]any{"duplicate_of": b.ID},
		"self":          map[string]any{"reason": "duplicate", "duplicate_of": a.ID},
		"other project": map[string]any{"reason": "duplicate", "duplicate_of": elsewhere.ID},
		"missing":       map[string]any{"reason": "duplicate", "duplicate_of": 9999},
		"bad reason":    map[string]any{"reason": "wontfix"},
	} {
		resp, out := h.do(t, http.MethodPost, path, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", name, resp.StatusCode, out)
		}
	}
	mark := h.maxEvent(t)
	closed := h.must(t, http.StatusOK, http.MethodPost, path, map[string]any{"reason": "duplicate", "duplicate_of": b.ID})
	if closed.DuplicateOf == nil || *closed.DuplicateOf != b.ID || closed.CloseReason != "duplicate" {
		t.Errorf("closed = %+v", closed)
	}
	evs := h.issueEvents(t, mark)
	if len(evs) != 1 || !strings.Contains(string(evs[0].Payload), fmt.Sprintf(`"duplicate_of":%d`, b.ID)) {
		t.Errorf("state_changed = %v", evs)
	}
	reopened := h.must(t, http.StatusOK, http.MethodPost, fmt.Sprintf("/v1/issues/%d/reopen", a.ID), nil)
	if reopened.DuplicateOf != nil {
		t.Errorf("reopen kept duplicate_of %d", *reopened.DuplicateOf)
	}
	// Omitting duplicate_of on a duplicate close is valid, as on GitHub.
	h.must(t, http.StatusOK, http.MethodPost, path, map[string]any{"reason": "duplicate"})
}

func TestIssueListFilters(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	a := h.create(t, map[string]any{"title": "alpha", "body": "100% broken", "labels": []string{"bug", "ui"}, "kind": "bug"})
	b := h.create(t, map[string]any{"title": "beta", "body": "100 broken", "labels": []string{"bug"}})
	c := h.create(t, map[string]any{"title": "gamma_x", "labels": []string{"ui"}})
	h.create(t, map[string]any{"title": "elsewhere", "project_id": h.otherID})
	// Touch a, then close c: c is the most recently updated, then a.
	h.must(t, http.StatusOK, http.MethodPatch, fmt.Sprintf("/v1/issues/%d", a.ID), map[string]any{"version": 1, "priority": 1})
	h.must(t, http.StatusOK, http.MethodPost, fmt.Sprintf("/v1/issues/%d/close", c.ID), nil)

	list := func(q string) []int64 {
		t.Helper()
		resp, out := h.do(t, http.MethodGet, "/v1/issues?"+q, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list ?%s = %d: %s", q, resp.StatusCode, out)
		}
		if strings.Contains(string(out), `"body"`) {
			t.Errorf("list ?%s rows carry a body: %s", q, out)
		}
		var rows []issueRowBody
		if err := json.Unmarshal(out, &rows); err != nil {
			t.Fatalf("decode list: %v: %s", err, out)
		}
		ids := []int64{}
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return ids
	}
	p := fmt.Sprintf("project_id=%d", h.pid)
	for q, want := range map[string][]int64{
		p:                                    {c.ID, a.ID, b.ID},
		p + "&sort=created":                  {c.ID, b.ID, a.ID},
		p + "&state=open":                    {a.ID, b.ID},
		p + "&state=open&state=closed":       {c.ID, a.ID, b.ID},
		p + "&label=bug&label=UI":            {a.ID},
		p + "&label=bug":                     {a.ID, b.ID},
		p + "&kind=bug":                      {a.ID},
		p + "&q=100%25":                      {a.ID},
		p + "&q=_x":                          {c.ID},
		p + "&source=local":                  {c.ID, a.ID, b.ID},
		p + "&source=github":                 {},
		p + "&sort=created&limit=1&offset=1": {b.ID},
		p + "&sort=created&offset=2":         {a.ID},
	} {
		if got := list(q); !slices.Equal(got, want) {
			t.Errorf("?%s = %v, want %v", q, got, want)
		}
	}
	for _, q := range []string{"state=bogus", "sort=title", "source=gitlab", "limit=-1", "offset=x", "project_id=x"} {
		resp, out := h.do(t, http.MethodGet, "/v1/issues?"+q, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("?%s = %d, want 400: %s", q, resp.StatusCode, out)
		}
	}
}

// TestIssueListByRemoteNumber is the lookup that replaced the `github_issue`
// create field (task 130.11, decision 22.4): `remote_number` answers only the
// issue imported from that GitHub number in the named project, composes with
// the other filters, and is refused without project_id.
func TestIssueListByRemoteNumber(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	imp := func(pid int64, key string, number int, title string) *store.Issue {
		t.Helper()
		iss, _, err := h.st.UpsertRemoteIssue(t.Context(), store.RemoteIssue{
			ProjectID: pid, Provider: "github", RemoteKey: key, Repo: "o/r", Number: number,
			URL: fmt.Sprintf("https://github.com/o/r/issues/%d", number), RemoteJSON: `{"state":"OPEN"}`,
			Title: title, State: issuestate.Open,
		}, issuestate.Sync)
		if err != nil {
			t.Fatalf("UpsertRemoteIssue: %v", err)
		}
		return iss
	}
	seven := imp(h.pid, "I_7", 7, "seven")
	imp(h.pid, "I_8", 8, "eight")
	imp(h.otherID, "I_7_other", 7, "seven elsewhere")
	// A local issue whose id happens to be a GitHub number must not answer.
	h.create(t, map[string]any{"title": "local"})

	list := func(q string) []int64 {
		t.Helper()
		resp, out := h.do(t, http.MethodGet, "/v1/issues?"+q, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list ?%s = %d: %s", q, resp.StatusCode, out)
		}
		var rows []issueRowBody
		if err := json.Unmarshal(out, &rows); err != nil {
			t.Fatalf("decode list: %v: %s", err, out)
		}
		ids := []int64{}
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return ids
	}
	p := fmt.Sprintf("project_id=%d", h.pid)
	for q, want := range map[string][]int64{
		p + "&remote_number=7":              {seven.ID},
		p + "&remote_number=9":              {},
		p + "&remote_number=7&state=closed": {},
		p + "&remote_number=7&state=open":   {seven.ID},
	} {
		if got := list(q); !slices.Equal(got, want) {
			t.Errorf("?%s = %v, want %v", q, got, want)
		}
	}
	// A deleted issue's tombstone never answers.
	if resp, out := h.do(t, http.MethodDelete, fmt.Sprintf("/v1/issues/%d", seven.ID), nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", resp.StatusCode, out)
	}
	if got := list(p + "&remote_number=7"); len(got) != 0 {
		t.Errorf("a deleted issue still answers its number: %v", got)
	}
	for _, q := range []string{"remote_number=7", p + "&remote_number=0", p + "&remote_number=x"} {
		resp, out := h.do(t, http.MethodGet, "/v1/issues?"+q, nil)
		wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
		if !strings.Contains(string(out), "remote_number") {
			t.Errorf("?%s: the 400 does not name remote_number: %s", q, out)
		}
	}
}

func TestIssueLabelCatalogue(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	h.create(t, map[string]any{"title": "a", "labels": []string{"bug", "ui"}})
	h.create(t, map[string]any{"title": "b", "labels": []string{"Bug"}})
	resp, out := h.do(t, http.MethodGet, fmt.Sprintf("/v1/projects/%d/issue-labels", h.pid), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("labels = %d: %s", resp.StatusCode, out)
	}
	var labels []issueLabelBody
	if err := json.Unmarshal(out, &labels); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []issueLabelBody{{Name: "bug", Source: "local", IssueCount: 2}, {Name: "ui", Source: "local", IssueCount: 1}}
	if !slices.Equal(labels, want) {
		t.Errorf("labels = %+v, want %+v", labels, want)
	}
	resp, out = h.do(t, http.MethodGet, "/v1/projects/999/issue-labels", nil)
	wantError(t, resp, out, http.StatusNotFound, CodeNotFound)
}

func TestIssueGetReportsActiveRootTasks(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	iss := h.create(t, map[string]any{"title": "work"})
	mk := func(title string, state store.TaskState, parent *int64) *store.Task {
		task := &store.Task{
			ProjectID: h.pid, Title: title, WorkflowName: "w", WorkflowSnapshot: "x",
			BaseBranch: "main", BranchName: "b-" + title, State: state, IssueID: &iss.ID, ParentTaskID: parent,
		}
		if err := h.st.CreateTask(t.Context(), task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task
	}
	live := mk("live", store.TaskQueued, nil)
	mk("done", store.TaskDone, nil)
	mk("lane", store.TaskQueued, &live.ID)
	got := h.must(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/v1/issues/%d", iss.ID), nil)
	if got.Tasks.Count != 2 || !slices.Equal(got.Tasks.ActiveIDs, []int64{live.ID}) || !got.Active {
		t.Errorf("tasks = %+v active %v, want two roots with only %d active", got.Tasks, got.Active, live.ID)
	}
}

func TestIssueCreateIdempotency(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	body := map[string]any{"project_id": h.pid, "title": "once"}
	post := func(b any, key string) (*http.Response, []byte) {
		return h.do(t, http.MethodPost, "/v1/issues", b, "Idempotency-Key", key)
	}
	count := func() int64 {
		rows, err := h.st.TableRows(t.Context())
		if err != nil {
			t.Fatalf("TableRows: %v", err)
		}
		return rows["issues"]
	}

	resp, out := post(body, "k-1")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first = %d: %s", resp.StatusCode, out)
	}
	var first issueBody
	_ = json.Unmarshal(out, &first)
	resp, out = post(map[string]any{"title": "once", "project_id": h.pid}, "k-1")
	var replay issueBody
	_ = json.Unmarshal(out, &replay)
	if resp.StatusCode != http.StatusCreated || replay.ID != first.ID {
		t.Errorf("replay = %d id %d, want 201 with issue %d", resp.StatusCode, replay.ID, first.ID)
	}
	if n := count(); n != 1 {
		t.Errorf("issues = %d after a replay, want 1", n)
	}

	resp, out = post(map[string]any{"project_id": h.pid, "title": "different"}, "k-1")
	if d := conflict(t, resp, out); detailString(t, d, "reason") != idempotencyReasonReused {
		t.Errorf("reuse = %s", out)
	}
	if n := count(); n != 1 {
		t.Errorf("issues = %d after a reused key, want 1", n)
	}

	// The same key on POST /v1/tasks is a different scope.
	if _, err := h.st.GetIdempotencyKey(t.Context(), http.MethodPost, "/v1/tasks", "k-1"); err == nil {
		t.Error("an issue key was recorded in the task table")
	}

	// Deleting the issue takes its key with it, so the key creates afresh.
	h.do(t, http.MethodDelete, fmt.Sprintf("/v1/issues/%d", first.ID), nil)
	resp, out = post(body, "k-1")
	var fresh issueBody
	_ = json.Unmarshal(out, &fresh)
	if resp.StatusCode != http.StatusCreated || fresh.ID == first.ID {
		t.Errorf("after delete = %d id %d, want a new issue", resp.StatusCode, fresh.ID)
	}

	// The 24-hour prune covers the issue table too.
	n, err := h.st.PruneIdempotencyKeys(t.Context(), time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Errorf("prune = %d, %v, want the one issue key", n, err)
	}
	if _, err := h.st.GetIssueIdempotencyKey(t.Context(), http.MethodPost, idempotencyIssueRoute, "k-1"); err == nil {
		t.Error("the issue key survived the prune")
	}
}

// TestIssueEventsOnTheStream: issue.* rides GET /v1/events unchanged —
// filtered by type and project, and resumed by Last-Event-ID.
func TestIssueEventsOnTheStream(t *testing.T) {
	h := newSSEHarness(t)
	other := &store.Project{Name: "other", Path: "/elsewhere", DefaultBranch: "main"}
	if err := h.st.CreateProject(t.Context(), other); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	create := func(pid int64, title string) *store.Issue {
		iss, err := h.st.CreateIssue(t.Context(), store.NewIssue{ProjectID: pid, Title: title, Body: "secret body"}, issuestate.Human)
		if err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		return iss
	}
	cursor, err := h.st.MaxEventID(t.Context())
	if err != nil || cursor == 0 {
		t.Fatalf("MaxEventID = %d, %v: the harness's task should have written one", cursor, err)
	}
	mine := create(h.projectID, "mine")
	create(other.ID, "theirs")
	url := fmt.Sprintf("%s/v1/events?types=%s,%s&project_id=%d", h.ts.URL,
		store.EventIssueCreated, store.EventIssueStateChanged, h.projectID)
	c := openSSE(t, url, fmt.Sprint(cursor))
	f := c.next(t)
	if f.event != store.EventIssueCreated || !strings.Contains(f.data, fmt.Sprintf(`"id":%d`, mine.ID)) {
		t.Fatalf("replayed frame = %+v", f)
	}
	if strings.Contains(f.data, "secret") {
		t.Errorf("frame carries the body: %s", f.data)
	}
	c.expectNone(t, 200*time.Millisecond)
	c.close()

	if _, err := h.st.TransitionIssue(t.Context(), mine.ID, issuestate.Close, "", nil, issuestate.Human); err != nil {
		t.Fatalf("close: %v", err)
	}
	re := openSSE(t, url, f.id)
	if g := re.next(t); g.event != store.EventIssueStateChanged {
		t.Errorf("resumed frame = %+v, want the close", g)
	}
	re.expectNone(t, 300*time.Millisecond)
}

// TestIssueToolsOverMCP: a step's agent files an issue as itself — author
// `task N`, created_by_task_id, actor agent — and the shared endpoint is an
// agent too, with no task to name.
func TestIssueToolsOverMCP(t *testing.T) {
	t.Parallel()
	h := newIssueHarness(t)
	creator := &store.Task{
		ProjectID: h.pid, Title: "creator", WorkflowName: "w", WorkflowSnapshot: "x",
		BaseBranch: "main", BranchName: "b", State: store.TaskRunning,
	}
	if err := h.st.CreateTask(t.Context(), creator, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	sess, err := h.srv.MCP().OpenStep(1, creator.ID, "plan")
	if err != nil {
		t.Fatalf("OpenStep: %v", err)
	}
	call := func(endpoint, token, tool string, args map[string]any) map[string]any {
		t.Helper()
		// A fresh client per call: wrapping the server's shared one twice
		// would let the first token win.
		hc := &http.Client{Transport: bearerRoundTripper{base: http.DefaultTransport, token: token}}
		client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
		cs, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: h.ts.URL + endpoint, HTTPClient: hc}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", endpoint, err)
		}
		defer func() { _ = cs.Close() }()
		res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		text := res.Content[0].(*sdk.TextContent).Text
		if res.IsError {
			t.Fatalf("%s failed: %s", tool, text)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("decode %s: %v: %s", tool, err, text)
		}
		return out
	}

	mark := h.maxEvent(t)
	args := map[string]any{"body": map[string]any{"project_id": h.pid, "title": "found a bug"}, "idempotency_key": "agent-1"}
	got := call(sess.URLPath(), sess.Secret, "issue_create", args)
	if got["author"] != fmt.Sprintf("task %d", creator.ID) || got["created_by_task_id"] != float64(creator.ID) {
		t.Errorf("step create = author %v created_by %v", got["author"], got["created_by_task_id"])
	}
	again := call(sess.URLPath(), sess.Secret, "issue_create", args)
	if again["id"] != got["id"] {
		t.Errorf("idempotent replay over MCP = issue %v, want %v", again["id"], got["id"])
	}
	evs := h.issueEvents(t, mark)
	if len(evs) != 1 || !strings.Contains(string(evs[0].Payload), `"by":"agent"`) {
		t.Errorf("events = %v, want one issue.created by agent", evs)
	}

	shared := call("/mcp", testToken, "issue_create", map[string]any{"body": map[string]any{"project_id": h.pid, "title": "t2"}})
	if shared["author"] != "agent" || shared["created_by_task_id"] != nil {
		t.Errorf("shared create = author %v created_by %v", shared["author"], shared["created_by_task_id"])
	}
	closed := call("/mcp", testToken, "issue_close", map[string]any{"id": shared["id"], "body": map[string]any{"reason": "not_planned"}})
	if closed["state"] != "closed" {
		t.Errorf("issue_close = %v", closed)
	}

	// A comment's author follows create's rule (task 130 decision 24.6).
	comment := map[string]any{"id": got["id"], "body": map[string]any{"body": "from the step"}}
	if c := call(sess.URLPath(), sess.Secret, "issue_comment", comment); c["author"] != fmt.Sprintf("task %d", creator.ID) {
		t.Errorf("step comment author = %v", c["author"])
	}
	comment["body"] = map[string]any{"body": "from the shared endpoint"}
	if c := call("/mcp", testToken, "issue_comment", comment); c["author"] != "agent" {
		t.Errorf("shared comment author = %v", c["author"])
	}
	thread, _ := call("/mcp", testToken, "issue_comments", map[string]any{"id": got["id"]})["comments"].([]any)
	if len(thread) != 2 {
		t.Errorf("issue_comments = %v, want two", thread)
	}
}
