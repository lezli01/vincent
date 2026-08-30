package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
)

// PATCH /v1/config (task 060). The invariants asserted here are the ones the
// endpoint's usefulness rests on: nothing is written when the patch does not
// hold, comments survive when it does, and a GET immediately after a 200 sees
// the new value with no sleep.

// configHarness is a server whose config dir is a real directory holding the
// shipped template, wired to an applier that behaves like the daemon's.
type configHarness struct {
	ts   *httptest.Server
	path string
	cur  atomic.Pointer[config.Config]
	// applied counts synchronous applies, so a test can tell "the daemon put
	// it into force" apart from "the file happens to say so".
	applied atomic.Int64
}

func newConfigHarness(t *testing.T) *configHarness {
	t.Helper()
	dir := t.TempDir()
	if _, err := config.EnsureDefaultFile(dir); err != nil {
		t.Fatalf("seed config.yaml: %v", err)
	}
	h := &configHarness{path: filepath.Join(dir, config.FileName)}
	initial := config.Default()
	h.cur.Store(&initial)
	s := New(Deps{
		Token:       testToken,
		Config:      func() config.Config { return *h.cur.Load() },
		StartedAt:   time.Now().Add(-time.Minute),
		ListenAddr:  "127.0.0.1:12345",
		Dirs:        config.Dirs{Config: dir},
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		ApplyConfig: func(next config.Config) {
			// The daemon pins listen across a reload; so does this.
			next.Listen = h.cur.Load().Listen
			h.cur.Store(&next)
			h.applied.Add(1)
		},
	})
	h.ts = httptest.NewServer(s.Handler())
	t.Cleanup(h.ts.Close)
	return h
}

func (h *configHarness) patch(t *testing.T, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, h.ts.URL+"/v1/config", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, out
}

func (h *configHarness) bytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(h.path)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	return b
}

// The read-after-write decision 5 exists for: a GET issued the instant a 200
// lands must see the new value, with no retry loop and no sleep.
func TestConfigPatchAppliesBeforeItAnswers(t *testing.T) {
	h := newConfigHarness(t)
	resp, body := h.patch(t, `{"max_parallel_tasks":9,"log_level":"debug"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	var answered configResponse
	if err := json.Unmarshal(body, &answered); err != nil {
		t.Fatalf("parse patch response: %v", err)
	}
	if answered.MaxParallelTasks != 9 || answered.LogLevel != "debug" {
		t.Errorf("the patch response does not carry the new values: %+v", answered)
	}
	if h.applied.Load() == 0 {
		t.Error("the daemon's applier was never called; the change is on disk and not in force")
	}
	_, getBody := doRequest(t, h.ts, http.MethodGet, "/v1/config", testToken)
	var got configResponse
	if err := json.Unmarshal(getBody, &got); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if got.MaxParallelTasks != 9 || got.LogLevel != "debug" {
		t.Errorf("GET right after the 200 still reads the old config: %+v", got)
	}
}

// The file is documentation as much as it is settings, so an edit that
// flattens it is a regression even when the values are right.
func TestConfigPatchKeepsCommentsAndUncommentsInPlace(t *testing.T) {
	h := newConfigHarness(t)
	before := h.bytes(t)
	resp, body := h.patch(t, `{"notify":{"on":["blocked"],"command":["/bin/echo","hi"]}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	after := string(h.bytes(t))
	if n := strings.Count(after, "\nnotify:"); n != 1 {
		t.Errorf("notify: appears %d times, want 1:\n%s", n, after)
	}
	if !strings.Contains(after, "WARNING: this is arbitrary code the daemon runs as you") {
		t.Error("the notify block's documentation was flattened")
	}
	// Every comment the file had, it still has.
	for _, line := range strings.Split(string(before), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") || strings.Contains(trimmed, "notify") ||
			strings.Contains(trimmed, "on:") || strings.Contains(trimmed, "command:") {
			continue
		}
		if !strings.Contains(after, trimmed) {
			t.Errorf("a comment was lost: %q", trimmed)
		}
	}
}

// "An invalid patch writes nothing" is asserted on the bytes, not on a
// reload: a file that was written and then reverted is still a file that was
// written, and a crash between the two would leave the bad one.
func TestConfigPatchRejectionLeavesTheFileByteIdentical(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"a port that is not a port", `{"listen":"127.0.0.1:notaport"}`},
		{"a host that is not loopback", `{"listen":"0.0.0.0:0"}`},
		{"a cap below one", `{"max_parallel_tasks":0}`},
		{"an unknown log level", `{"log_level":"chatty"}`},
		{"a duration that will not parse", `{"defaults":{"agent_timeout":"soon"}}`},
		{"a branch template that does not compile", `{"branch_template":"vincent/{{.ID"}`},
		{"a notify state that is not one", `{"notify":{"on":["exploded"]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newConfigHarness(t)
			before := h.bytes(t)
			resp, body := h.patch(t, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, body)
			}
			var env errorBody
			if err := json.Unmarshal(body, &env); err != nil || env.Error.Code != CodeValidationFailed {
				t.Errorf("want the snake_case validation envelope, got %s", body)
			}
			if !bytes.Equal(before, h.bytes(t)) {
				t.Error("a rejected patch changed config.yaml")
			}
			if h.applied.Load() != 0 {
				t.Error("a rejected patch was applied")
			}
		})
	}
}

// Two patches that interleave would produce a file holding neither. The mutex
// is what stops it; this is what would notice if it were removed.
func TestConfigPatchesSerialize(t *testing.T) {
	h := newConfigHarness(t)
	const n = 12
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"max_parallel_tasks":` + itoaForTest(i+1) + `}`
			if i%2 == 1 {
				body = `{"transcript_retention_days":` + itoaForTest(i+1) + `}`
			}
			resp, out := h.patch(t, body)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d (body %s)", resp.StatusCode, out)
			}
		}()
	}
	wg.Wait()
	if _, err := config.Decode(h.bytes(t)); err != nil {
		t.Fatalf("the file does not parse after concurrent patches: %v\n%s", err, h.bytes(t))
	}
}

// `listen` is written and does not take effect until a restart. The response
// has to say what is in force, not what was asked for, or a client will show
// an address nothing is bound to.
func TestConfigPatchReportsListenAsPendingUntilRestart(t *testing.T) {
	h := newConfigHarness(t)
	resp, body := h.patch(t, `{"listen":"127.0.0.1:9999"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	var got configResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Listen != config.Default().Listen {
		t.Errorf("listen = %q, want the address still in force (%q)", got.Listen, config.Default().Listen)
	}
	if !strings.Contains(string(h.bytes(t)), "listen: 127.0.0.1:9999") {
		t.Error("listen was not written to config.yaml")
	}
}

// An empty patch is the configuration unchanged, not an error and not a write.
func TestConfigPatchWithNoKeysWritesNothing(t *testing.T) {
	h := newConfigHarness(t)
	before := h.bytes(t)
	resp, body := h.patch(t, `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	if !bytes.Equal(before, h.bytes(t)) {
		t.Error("an empty patch rewrote the file")
	}
}

// The drift this task exists because of: GET /v1/config served eleven of the
// twenty-odd keys config.Config carries, so nine of them were invisible from
// every client. This fails when a field is added to config.Config and not to
// the wire shape.
func TestConfigResponseCoversEveryConfigField(t *testing.T) {
	served := map[string]bool{}
	for _, f := range reflect.VisibleFields(reflect.TypeOf(configResponse{})) {
		if tag := f.Tag.Get("json"); tag != "" {
			served[strings.Split(tag, ",")[0]] = true
		}
	}
	var missing []string
	for _, f := range reflect.VisibleFields(reflect.TypeOf(config.Config{})) {
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if !served[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("config.Config carries keys GET /v1/config does not serve, so no client can see them: %v\n"+
			"add them to configResponse, configBody and configPatch", missing)
	}
}

// Every key the read serves has to be reachable by the patch, or the endpoint
// shows something no client can change — which is the read-only state this
// task ended.
func TestConfigPatchCoversEveryServedKey(t *testing.T) {
	patchable := map[string]bool{}
	for _, f := range reflect.VisibleFields(reflect.TypeOf(configPatch{})) {
		if tag := f.Tag.Get("json"); tag != "" {
			patchable[strings.Split(tag, ",")[0]] = true
		}
	}
	var missing []string
	for _, f := range reflect.VisibleFields(reflect.TypeOf(configResponse{})) {
		tag := f.Tag.Get("json")
		if tag == "" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if !patchable[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("GET /v1/config serves keys PATCH cannot change: %v", missing)
	}
}

func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
