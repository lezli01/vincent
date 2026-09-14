package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/trigger"
)

// `vincent trigger test` against the real trigger routes over httptest: the
// judgement it prints is the daemon's, decoded through apiclient, so a wire
// rename on either side shows up here as a missing stage.

// triggerFixture is a disabled command trigger with both filters, so each
// stage the command prints can be made to pass or stop an event.
func triggerFixture(project int64) string {
	return "id: triage\n" +
		"enabled: false\n" +
		"source:\n" +
		"  type: command\n" +
		"  project: " + strconv.FormatInt(project, 10) + "\n" +
		"  poll_interval: 5m\n" +
		"  command: [\"never-run\"]\n" +
		"match:\n" +
		"  kind: bug\n" +
		"if: '{{ eq .Event.priority \"high\" }}'\n" +
		"action:\n" +
		"  type: create_task\n" +
		"  title: \"Triage {{ .Event.title }}\"\n"
}

// newTriggerTestDaemon publishes a daemon whose trigger routes are real and
// returns its data dir.
func newTriggerTestDaemon(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	token, err := daemon.EnsureToken(dataDir)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	project := &store.Project{Name: "live", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "triggers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "triage.yaml"), []byte(triggerFixture(project.ID)), 0o600); err != nil {
		t.Fatalf("write trigger: %v", err)
	}
	reg := trigger.NewRegistry(dir, nil)
	reg.Reload()
	if e, ok := reg.Get("triage"); !ok || !e.Valid() {
		t.Fatalf("fixture trigger did not load: %v", e.Errors)
	}
	// A key the ledger already holds, for the dedupe stage.
	if _, err := st.RecordTriggerDelivery(ctx, &store.TriggerDelivery{
		TriggerID: "triage", EventID: "dup", DedupeKey: "dup", Outcome: store.DeliveryFired, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("record delivery: %v", err)
	}

	var srv atomic.Pointer[api.Server]
	mgr := trigger.NewManager(trigger.Deps{
		Store: st,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			srv.Load().Inner().ServeHTTP(w, r)
		}),
		Registry: reg,
	})
	s := api.New(api.Deps{
		Token:           token,
		Config:          config.Default,
		StartedAt:       time.Now(),
		ListenAddr:      "127.0.0.1:0",
		RequestStop:     func() {},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:           st,
		Triggers:        mgr,
		TriggerRegistry: reg,
		TriggerWriter:   trigger.NewWriter(dir),
	})
	srv.Store(s)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	publishDaemon(t, dataDir, ts.URL)
	return dataDir
}

// runTriggerInDataDir runs `vincent trigger ...` against a data dir the caller
// owns.
func runTriggerInDataDir(t *testing.T, dataDir string, args ...string) (string, int) {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv(config.EnvDataDir, dataDir)
	var buf bytes.Buffer
	root := newRootCmd()
	root.SilenceErrors = true
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append([]string{"trigger"}, args...))
	code := asExitCode(root.ExecuteContext(context.Background()))
	return buf.String(), code
}

func writeEvent(t *testing.T, event string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(event), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestTriggerTestCommandRendersTheJudgement(t *testing.T) {
	dataDir := newTriggerTestDaemon(t)

	for _, c := range []struct {
		name  string
		event string
		code  int
		want  []string
	}{
		{
			name:  "would fire",
			event: `{"id":"bug-1","kind":"bug","priority":"high","title":"Crash"}`,
			want: []string{
				"trigger triage, event bug-1", "match:    matched", "if:       true",
				"dedupe:   bug-1 (new)", "action:   create_task POST /v1/tasks",
				`"title": "Triage Crash"`, `"paused": true`, "outcome:  fired: the action would be replayed",
			},
		},
		{
			name:  "already delivered",
			event: `{"id":"dup","kind":"bug","priority":"high","title":"Again"}`,
			want:  []string{"dedupe:   dup (already delivered: would dedupe)", "outcome:  deduped", "action:   -"},
		},
		{
			name:  "match miss",
			event: `{"id":"c-1","kind":"chore","priority":"high","title":"Tidy"}`,
			want:  []string{"match:    no match (kind)", "if:       -", "dedupe:   -", "action:   -", "outcome:  filtered"},
		},
		{
			name:  "guard says no",
			event: `{"id":"b-2","kind":"bug","priority":"low","title":"Minor"}`,
			want:  []string{"match:    matched", "if:       false", "dedupe:   -", "outcome:  filtered"},
		},
		{
			// The title template reads a key the event lacks: a render
			// failure is the one outcome that exits 1.
			name:  "template error",
			event: `{"id":"b-3","kind":"bug","priority":"high"}`,
			code:  1,
			want:  []string{"outcome:  error", "error:    "},
		},
	} {
		out, code := runTriggerInDataDir(t, dataDir, "test", "triage", "--event", writeEvent(t, c.event))
		if code != c.code {
			t.Errorf("%s: exit %d, want %d:\n%s", c.name, code, c.code, out)
		}
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: output is missing %q:\n%s", c.name, w, out)
			}
		}
	}
}

func TestTriggerTestCommandJSON(t *testing.T) {
	dataDir := newTriggerTestDaemon(t)
	out, code := runTriggerInDataDir(t, dataDir, "test", "triage", "--json",
		"--event", writeEvent(t, `{"id":"bug-1","kind":"bug","priority":"high","title":"Crash"}`))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var j apiclient.TriggerJudgement
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		t.Fatalf("--json is not a judgement: %v (%s)", err, out)
	}
	if !j.Matched || j.If == nil || !*j.If || j.DedupeKey != "bug-1" || j.WouldDedupe ||
		j.Outcome != apiclient.TriggerFired || j.Action == nil || j.Action.Path != "/v1/tasks" {
		t.Fatalf("judgement = %+v", j)
	}
	var body map[string]any
	if err := json.Unmarshal(j.Action.Body, &body); err != nil || body["title"] != "Triage Crash" {
		t.Errorf("action body = %s (%v), want the rendered title", j.Action.Body, err)
	}
}

func TestTriggerTestCommandRefusals(t *testing.T) {
	dataDir := newTriggerTestDaemon(t)
	event := writeEvent(t, `{"id":"bug-1","kind":"bug","priority":"high","title":"Crash"}`)

	out, code := runTriggerInDataDir(t, dataDir, "test", "nope", "--event", event)
	if code != 1 || !strings.Contains(out, "no such trigger: nope") {
		t.Errorf("unknown trigger: exit %d, want 1 with the daemon's message:\n%s", code, out)
	}

	// A fixture that is not one object is refused before any request, so it
	// is exit 1 even with no daemon to ask.
	noDaemon := t.TempDir()
	for name, fixture := range map[string]string{
		"array":      `[{"id":"x"}]`,
		"null":       `null`,
		"two values": `{"id":"a"} {"id":"b"}`,
		"not JSON":   `id: a`,
	} {
		out, code := runTriggerInDataDir(t, noDaemon, "test", "triage", "--event", writeEvent(t, fixture))
		if code != 1 || !strings.Contains(out, "--event") {
			t.Errorf("%s fixture: exit %d, want 1 naming --event:\n%s", name, code, out)
		}
	}
	out, code = runTriggerInDataDir(t, noDaemon, "test", "triage", "--event", filepath.Join(noDaemon, "missing.json"))
	if code != 1 {
		t.Errorf("missing fixture file: exit %d, want 1:\n%s", code, out)
	}
	if _, code := runTriggerInDataDir(t, dataDir, "test", "triage"); code == 0 {
		t.Error("test without --event exited 0")
	}
}
