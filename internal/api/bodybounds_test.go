package api

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// postRaw sends an authenticated POST with the body verbatim, which is what a
// bound has to be proven against: json.Marshal cannot express two concatenated
// documents, and a marshalled body is by construction one the decoder accepts.
func (h *projectHarness) postRaw(t *testing.T, path, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
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

// oversizeBytes is far above any bound this could plausibly be fixed with, so
// the assertion is "a bound exists" rather than a guess at its value — the same
// device internal/workflow's registry bounds test uses for §5.2.
const oversizeBytes = 32 << 20

// TestRequestOneJSONDocument pins the first half of issue #140: a request body
// is exactly one JSON document.
//
// decodeJSON (internal/api/projects.go) runs a single json.Decoder.Decode and
// returns, so everything after the first value is discarded unread: a client
// that sends two documents has the first one acted on and is told nothing.
func TestRequestOneJSONDocument(t *testing.T) {
	t.Run("concatenated documents", func(t *testing.T) {
		h, one := newBodyBoundsHarness(t)
		resp, out := h.postRaw(t, "/v1/projects", one+one)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("two JSON documents: status %d, want 400 — body %s", resp.StatusCode, out)
		}
		wantError(t, resp, out, http.StatusBadRequest, CodeInvalidJSON)
	})

	t.Run("trailing garbage", func(t *testing.T) {
		h, one := newBodyBoundsHarness(t)
		resp, out := h.postRaw(t, "/v1/projects", one+" nonsense")
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("trailing garbage: status %d, want 400 — body %s", resp.StatusCode, out)
		}
		wantError(t, resp, out, http.StatusBadRequest, CodeInvalidJSON)
	})

	t.Run("trailing whitespace stays valid", func(t *testing.T) {
		// The other side of the same rule: one document followed only by
		// whitespace is well-formed and must keep working.
		h, one := newBodyBoundsHarness(t)
		resp, out := h.postRaw(t, "/v1/projects", one+"\n\t ")
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("one document plus whitespace: status %d, want 201 — body %s", resp.StatusCode, out)
		}
	})
}

// TestRequestBodyBounded pins the second half of issue #140: nothing wraps the
// request body in an http.MaxBytesReader, so a body of any size is read into
// memory whole and acted on.
func TestRequestBodyBounded(t *testing.T) {
	t.Run("oversized json body", func(t *testing.T) {
		h, _ := newBodyBoundsHarness(t)
		repo := testrepo.Init(t, "main")
		body := `{"path":` + jsonQuote(repo) + `,"name":"` + strings.Repeat("x", oversizeBytes) + `"}`
		resp, out := h.postRaw(t, "/v1/projects", body)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("%d-byte body: status %d, want 413 — body %s", len(body), resp.StatusCode, head(out))
		}
	})

	t.Run("oversized workflow source", func(t *testing.T) {
		// §5.2 bounds one workflow source at 1 MiB where the registry
		// catalogues it. The validate endpoint parses the same artifact out of
		// a request body, so it must not be the way around that bound.
		h, _ := newBodyBoundsHarness(t)
		yaml := "name: big\ndescription: big\n# " + strings.Repeat("x", oversizeBytes) +
			"\nsteps:\n  - {id: gate, type: manual, instructions: fine}\n"
		resp, out := h.postRaw(t, "/v1/workflows/validate",
			`{"yaml":`+jsonQuote(yaml)+`}`)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("%d-byte workflow source: status %d, want 413 — body %s",
				len(yaml), resp.StatusCode, head(out))
		}
	})
}

// newBodyBoundsHarness returns a server with an empty store plus a valid
// one-document create body for it. Each subtest gets its own so a duplicate
// registration from a neighbour cannot stand in for the failure under test.
func newBodyBoundsHarness(t *testing.T) (*projectHarness, string) {
	t.Helper()
	repo := testrepo.Init(t, "main")
	return newProjectHarness(t), `{"path":` + jsonQuote(repo) + `}`
}

// jsonQuote quotes s as a JSON string literal without pushing the whole body
// through json.Marshal, which the concatenated cases cannot use.
func jsonQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// head keeps a failure message readable when the response echoes a body this
// large back.
func head(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}
