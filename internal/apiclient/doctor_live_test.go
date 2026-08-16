package apiclient_test

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/worktree"
)

// newDoctorClient wires the client to the **real** handlers over httptest,
// which is what keeps the two sides of §13.2 from drifting. The report types
// are aliases of internal/doctor's, so this test is about the JSON actually
// round-tripping through the daemon's encoder — a `json:"-"` or an unexported
// field would still lose data that compiles fine.
func newDoctorClient(t *testing.T) (*apiclient.Client, config.Dirs) {
	t.Helper()
	dirs := config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
	st, err := store.Open(filepath.Join(dirs.Data, "vincent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	agents := agent.NewRegistry(claude.New(func() string { return "/nonexistent/claude-not-here" }))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worktrees := worktree.NewManager(gitx.New(), dirs.Data)
	// Doctor's orphan report and `--fix` are gc's scan and gc's removal
	// (task 005), so the reclaimer is not optional wiring here — without it the
	// endpoint answers "orphans unknown", which is the no-daemon state.
	reclaimer := taskrun.NewReclaimer(taskrun.Deps{
		Store:     st,
		Config:    config.Default,
		Worktrees: worktrees,
		DataDir:   dirs.Data,
		Logger:    logger,
	})
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now().Add(-90 * time.Second),
		ListenAddr:  "127.0.0.1:7777",
		Dirs:        dirs,
		LogPath:     filepath.Join(dirs.Data, "logs", "daemon.log"),
		TailLog:     func(string, int) ([]string, error) { return []string{"line one", "line two"}, nil },
		RequestStop: func() {},
		Logger:      logger,
		Store:       st,
		Git:         gitx.New(),
		Worktrees:   worktrees,
		Reclaimer:   reclaimer,
		Agents:      agents,
		Catalog:     agent.NewCatalogCache(agents),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return apiclient.New(ts.URL, testToken), dirs
}

func TestDoctorRoundTripsEveryGroup(t *testing.T) {
	c, dirs := newDoctorClient(t)
	// A log the handler can stat, so the Log group carries more than a path.
	logPath := filepath.Join(dirs.Data, "logs", "daemon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("line one\nline two\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	rep, err := c.Doctor(t.Context())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if rep.GeneratedAt.IsZero() {
		t.Error("generated_at did not survive the wire")
	}
	if rep.Paths.DataDir != dirs.Data || rep.Paths.ConfigDir != dirs.Config {
		t.Errorf("paths = %+v", rep.Paths)
	}
	if rep.Daemon.Status != apiclient.DaemonRunning || rep.Daemon.Port != 7777 {
		t.Errorf("daemon = %+v", rep.Daemon)
	}
	if rep.Daemon.StartedAt == nil || rep.Daemon.UptimeSeconds < 60 {
		t.Errorf("daemon timing = %+v", rep.Daemon)
	}
	if !rep.Log.Exists || len(rep.Log.Tail) != 2 {
		t.Errorf("log = %+v", rep.Log)
	}
	if !rep.Database.Known || rep.Database.IntegrityCheck != "ok" {
		t.Errorf("database = %+v", rep.Database)
	}
	if !rep.Tasks.Known || rep.Tasks.Counts["queued"] != 0 {
		t.Errorf("tasks = %+v", rep.Tasks)
	}
	if len(rep.Agents) != 1 || rep.Agents[0].Available {
		t.Errorf("agents = %+v; the harness points claude at nothing", rep.Agents)
	}
	// The §9.5 tri-state has to survive JSON: an unresolvable binary is
	// "unknown", never a definite accusation.
	if rep.Agents[0].LoggedIn != nil {
		t.Errorf("logged_in = %v for an unresolvable binary, want null", *rep.Agents[0].LoggedIn)
	}
	if rep.Storage.DiskTotalBytes == 0 || rep.Storage.WorktreesDir == "" {
		t.Errorf("storage = %+v", rep.Storage)
	}
	if rep.Problems == nil {
		t.Error("problems is null; an empty list keeps `| jq '.problems[]'` working")
	}
	if !rep.Healthy() {
		t.Errorf("healthy harness reported problems: %v", rep.Problems)
	}
}

// TestDoctorFixRoundTrips proves the repair response's shape, including the
// fresh report that rides along so a client never needs a second call.
func TestDoctorFixRoundTrips(t *testing.T) {
	c, dirs := newDoctorClient(t)
	stray := filepath.Join(dirs.Data, doctor.WorktreesDirName, "4242")
	if err := os.MkdirAll(stray, 0o700); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}

	res, err := c.DoctorFix(t.Context(), true)
	if err != nil {
		t.Fatalf("DoctorFix: %v", err)
	}
	if res.Report == nil {
		t.Fatal("fix carried no report")
	}
	var removed, compacted bool
	for _, a := range res.Actions {
		switch a.Action {
		case apiclient.FixActionRemoveWorktree:
			removed = a.Status == apiclient.FixDone && a.Target == stray
		case apiclient.FixActionCompactDatabase:
			compacted = a.Status == apiclient.FixDone
		}
	}
	if !removed {
		t.Errorf("actions = %+v, want the orphan removed", res.Actions)
	}
	if !compacted {
		t.Errorf("actions = %+v, want the database compacted", res.Actions)
	}
	if len(res.Report.Storage.Orphans) != 0 {
		t.Errorf("the report taken after the fix still lists orphans: %+v", res.Report.Storage.Orphans)
	}
}

// TestDoctorNeedsAuth: the report names paths, versions and a log tail, so it
// is exactly as private as everything else behind the bearer token (§13.1).
func TestDoctorNeedsAuth(t *testing.T) {
	c, _ := newDoctorClient(t)
	unauth := apiclient.New(c.BaseURL(), "wrong-token")
	if _, err := unauth.Doctor(t.Context()); err == nil {
		t.Fatal("Doctor succeeded without a valid token")
	}
	if _, err := unauth.DoctorFix(t.Context(), false); err == nil {
		t.Fatal("DoctorFix succeeded without a valid token")
	}
}
