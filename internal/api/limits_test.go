package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

// postTyped is postRaw with the Content-Type under test — including none at
// all, which the §13.1 policy accepts.
func postTyped(t *testing.T, h *projectHarness, path, contentType, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, out
}

// TestRequestContentType pins §13.1's lenient content-type rule: JSON in any
// spelling and no label at all are accepted, and only a body that says it is
// something else is refused.
func TestRequestContentType(t *testing.T) {
	accepted := []string{"", "application/json", "application/json; charset=utf-8", "text/json"}
	for _, ct := range accepted {
		t.Run("accepts "+labelOf(ct), func(t *testing.T) {
			repo := testrepo.Init(t, "main")
			h := newProjectHarness(t)
			resp, out := postTyped(t, h, "/v1/projects", ct, `{"path":`+jsonQuote(repo)+`}`)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("Content-Type %q: status %d, want 201 — body %s", ct, resp.StatusCode, out)
			}
		})
	}
	refused := []string{"application/x-www-form-urlencoded", "text/html", "multipart/form-data"}
	for _, ct := range refused {
		t.Run("refuses "+labelOf(ct), func(t *testing.T) {
			repo := testrepo.Init(t, "main")
			h := newProjectHarness(t)
			resp, out := postTyped(t, h, "/v1/projects", ct, `{"path":`+jsonQuote(repo)+`}`)
			wantError(t, resp, out, http.StatusUnsupportedMediaType, CodeUnsupportedMediaType)
		})
	}
}

func labelOf(ct string) string {
	if ct == "" {
		return "no content type"
	}
	return ct
}

// TestRequestBodyAtLimit pins the inclusive edge of the body bound, which is
// the half a 413 test cannot see: a body of exactly the limit is a good
// request, and one byte more is not. Padding is trailing whitespace, so the
// document itself is identical on both sides of the edge.
func TestRequestBodyAtLimit(t *testing.T) {
	body := func(repo string, size int) string {
		doc := `{"path":` + jsonQuote(repo) + `}`
		return doc + strings.Repeat(" ", size-len(doc))
	}
	t.Run("exactly at the limit", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		h := newProjectHarness(t)
		resp, out := h.postRaw(t, "/v1/projects", body(repo, maxRequestBytes))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%d-byte body: status %d, want 201 — body %s", maxRequestBytes, resp.StatusCode, head(out))
		}
	})
	t.Run("one byte over the limit", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		h := newProjectHarness(t)
		resp, out := h.postRaw(t, "/v1/projects", body(repo, maxRequestBytes+1))
		wantError(t, resp, out, http.StatusRequestEntityTooLarge, CodePayloadTooLarge)
	})
}

// TestRequestBodyMalformed covers the bodies that are neither one document nor
// over the bound: nothing at all, and a document that stops mid-way.
func TestRequestBodyMalformed(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"empty body", ""},
		{"truncated document", `{"path":`},
		{"whitespace only", "  \n\t"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newProjectHarness(t)
			resp, out := h.postRaw(t, "/v1/projects", tt.body)
			wantError(t, resp, out, http.StatusBadRequest, CodeInvalidJSON)
		})
	}
}

// TestFieldBounds pins the per-field bounds: a body inside the transport bound
// still cannot make one field the whole of it, and the 400 names the field and
// the limit without echoing what was sent.
func TestFieldBounds(t *testing.T) {
	for _, tt := range []struct {
		name, path, body, wantMsg string
	}{
		{
			name:    "task title",
			path:    "/v1/tasks",
			body:    `{"project_id":1,"title":` + jsonQuote(strings.Repeat("t", maxTitleBytes+1)) + `}`,
			wantMsg: "title must be at most",
		},
		{
			name:    "task description",
			path:    "/v1/tasks",
			body:    `{"project_id":1,"title":"ok","description":` + jsonQuote(strings.Repeat("d", maxDescriptionBytes+1)) + `}`,
			wantMsg: "description must be at most",
		},
		{
			name:    "one field value",
			path:    "/v1/tasks",
			body:    `{"project_id":1,"title":"ok","fields":{"k":` + jsonQuote(strings.Repeat("v", maxFieldValueBytes+1)) + `}}`,
			wantMsg: "fields[k] must be at most",
		},
		{
			name:    "field count",
			path:    "/v1/tasks",
			body:    `{"project_id":1,"title":"ok","fields":` + manyFields(maxFieldCount+1) + `}`,
			wantMsg: "fields must have at most",
		},
		{
			name:    "resolve title",
			path:    "/v1/resolve",
			body:    `{"workflow":"adhoc","title":` + jsonQuote(strings.Repeat("t", maxTitleBytes+1)) + `}`,
			wantMsg: "title must be at most",
		},
		{
			name:    "repair prompt",
			path:    "/v1/tasks/1/repair",
			body:    `{"prompt":` + jsonQuote(strings.Repeat("p", maxPromptBytes+1)) + `}`,
			wantMsg: "prompt must be at most",
		},
		{
			name:    "retry run override",
			path:    "/v1/tasks/1/retry",
			body:    `{"run_override":` + jsonQuote(strings.Repeat("r", maxCommandBytes+1)) + `}`,
			wantMsg: "run_override must be at most",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newProjectHarness(t)
			resp, out := h.postRaw(t, tt.path, tt.body)
			wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
			if !strings.Contains(string(out), tt.wantMsg) {
				t.Errorf("message %s does not mention %q", head(out), tt.wantMsg)
			}
		})
	}
}

// TestProjectNameBounded is the same rule on the one string a project carries
// that a client chooses freely.
func TestProjectNameBounded(t *testing.T) {
	repo := testrepo.Init(t, "main")
	h := newProjectHarness(t)
	resp, out := h.postRaw(t, "/v1/projects",
		`{"path":`+jsonQuote(repo)+`,"name":`+jsonQuote(strings.Repeat("n", maxNameBytes+1))+`}`)
	wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(out), "name must be at most") {
		t.Errorf("message %s does not name the bound", head(out))
	}
}

// TestWorkflowValidateSourceBound pins the validate endpoint to §5.2's source
// bound, on both sides: a source of exactly workflow.MaxSourceBytes still
// validates — the same inclusive edge the registry honours for a file — and a
// larger one is refused rather than parsed.
func TestWorkflowValidateSourceBound(t *testing.T) {
	source := func(size int) string {
		src := "name: big\ndescription: big\nsteps:\n  - {id: gate, type: manual, instructions: fine}\n# "
		return src + strings.Repeat("x", size-len(src))
	}
	t.Run("exactly at the bound", func(t *testing.T) {
		h := newProjectHarness(t)
		yaml := source(workflow.MaxSourceBytes)
		resp, out := h.postRaw(t, "/v1/workflows/validate", `{"yaml":`+jsonQuote(yaml)+`}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%d-byte source: status %d, want 200 — body %s", len(yaml), resp.StatusCode, head(out))
		}
		var got validateResponse
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("decode response: %v (%s)", err, head(out))
		}
		if !got.Valid {
			t.Errorf("a %d-byte workflow should still validate: %s", len(yaml), head(out))
		}
	})
	t.Run("one byte over the bound", func(t *testing.T) {
		h := newProjectHarness(t)
		yaml := source(workflow.MaxSourceBytes + 1)
		resp, out := h.postRaw(t, "/v1/workflows/validate", `{"yaml":`+jsonQuote(yaml)+`}`)
		wantError(t, resp, out, http.StatusRequestEntityTooLarge, CodePayloadTooLarge)
	})
}

// TestServerTimeoutPolicy pins §13.3's side of the timeout bargain. The read
// bounds are set, and WriteTimeout is deliberately **not**: it is a
// server-wide deadline, and the state and per-task streams are long-lived by
// contract, so setting one would sever every open stream at the deadline.
func TestServerTimeoutPolicy(t *testing.T) {
	s := New(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if s.httpSrv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout is unset")
	}
	if s.httpSrv.ReadTimeout <= 0 {
		t.Error("ReadTimeout is unset: a dribbled body holds a connection indefinitely")
	}
	if s.httpSrv.IdleTimeout <= 0 {
		t.Error("IdleTimeout is unset: an idle connection lives as long as the daemon")
	}
	if s.httpSrv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0: it would cut §13.3's streams", s.httpSrv.WriteTimeout)
	}
}

// manyFields renders a fields object with n distinct keys.
func manyFields(n int) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"k` + strconv.Itoa(i) + `":"v"`)
	}
	b.WriteByte('}')
	return b.String()
}
