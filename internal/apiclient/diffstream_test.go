package apiclient_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// DiffStream has no cap: a diff larger than Diff's 8 MiB bound arrives whole,
// because `vincent task diff` pipes it into `git apply` and a cut patch is a
// corrupt one (task 100 decision 4). Diff keeps its bound for the TUI.
func TestDiffStreamIsUncapped(t *testing.T) {
	const size = 9 << 20
	body := bytes.Repeat([]byte("+0123456789abcdef\n"), size/18+1)[:size]
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)
	c := apiclient.New(ts.URL, "token")

	rc, err := c.DiffStream(t.Context(), 1)
	if err != nil {
		t.Fatalf("DiffStream: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("streamed %d bytes, want all %d unchanged", len(got), len(body))
	}

	bounded, err := c.Diff(t.Context(), 1)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(bounded) != 8<<20 {
		t.Errorf("Diff read %d bytes, want its 8 MiB bound", len(bounded))
	}
}

// A refusal comes back as the daemon's *Error, as it does from Diff.
func TestDiffStreamReturnsTheDaemonError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_state","message":"task has no worktree yet"}}`)
	}))
	t.Cleanup(ts.Close)

	_, err := apiclient.New(ts.URL, "token").DiffStream(t.Context(), 1)
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("err = %#v, want a 409 *apiclient.Error", err)
	}
}
