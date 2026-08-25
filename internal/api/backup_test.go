package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// backupHarness is the API over a real store in a real data dir. Nothing here
// can be faked usefully: the endpoint's whole subject is a file on disk taken
// from a live SQLite connection.
type backupHarness struct {
	ts    *httptest.Server
	dirs  config.Dirs
	st    *store.Store
	inbox string // a scratch directory for archives
}

func newBackupHarness(t *testing.T) *backupHarness {
	t.Helper()
	dirs := config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
	st, err := store.Open(filepath.Join(dirs.Data, backup.DatabaseEntry))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// The two directory trees a backup carries besides the database.
	writeFile(t, filepath.Join(dirs.Data, "transcripts", "1", "0-1.jsonl"), `{"type":"start"}`+"\n")
	writeFile(t, filepath.Join(dirs.Config, "config.yaml"), "listen: \"127.0.0.1:0\"\n")
	writeFile(t, filepath.Join(dirs.Config, "workflows", "review.yaml"), "name: review\n")
	// And the running-identity files it must not.
	writeFile(t, filepath.Join(dirs.Data, "token"), "secret")

	s := New(Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now().Add(-time.Minute),
		ListenAddr:  "127.0.0.1:12345",
		Dirs:        dirs,
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &backupHarness{ts: ts, dirs: dirs, st: st, inbox: t.TempDir()}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func (h *backupHarness) post(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		h.ts.URL+"/v1/daemon/backup", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/daemon/backup: %v", err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, raw
}

func (h *backupHarness) take(t *testing.T, name string) (string, backupResponse) {
	t.Helper()
	dst := filepath.Join(h.inbox, name)
	resp, raw := h.post(t, dst)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("backup = %d: %s", resp.StatusCode, raw)
	}
	var got backupResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("backup body is not JSON: %v (%s)", err, raw)
	}
	return dst, got
}

// archiveNames lists an archive's entries.
func archiveNames(t *testing.T, archive string) []string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatalf("open %s: %v", archive, err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip %s: %v", archive, err)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatalf("read %s: %v", archive, err)
		}
		names = append(names, hdr.Name)
	}
}

// TestBackupWritesAnOpenableDatabase is the acceptance criterion: the copy in
// the archive is a database, not a snapshot of a WAL-mode file missing its
// recent commits — and there is no `-wal`/`-shm` beside it, because
// VACUUM INTO emits one self-contained file.
func TestBackupWritesAnOpenableDatabase(t *testing.T) {
	h := newBackupHarness(t)
	if err := h.st.AppendEvent(t.Context(), &store.Event{Type: store.EventDaemonShuttingDown}); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	dst, got := h.take(t, "backup.tar.gz")
	if got.Path != dst || got.Bytes <= 0 || got.DatabaseBytes <= 0 {
		t.Errorf("response = %+v, want the path and non-zero sizes", got)
	}
	if got.SchemaVersion != store.NewestMigration() {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, store.NewestMigration())
	}
	if got.CreatedAt == "" {
		t.Error("created_at is empty")
	}

	names := archiveNames(t, dst)
	for _, name := range names {
		if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			t.Errorf("archive carries %q; VACUUM INTO emits a single file", name)
		}
		if name == "token" {
			t.Error("archive carries the API token")
		}
	}

	into := backup.Dirs{Data: t.TempDir(), Config: t.TempDir()}
	if _, err := backup.Restore(dst, into, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	copied, err := store.Open(filepath.Join(into.Data, backup.DatabaseEntry))
	if err != nil {
		t.Fatalf("the archived database does not open: %v", err)
	}
	defer func() { _ = copied.Close() }()
	verdict, err := copied.IntegrityCheck(t.Context())
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	if verdict != "ok" {
		t.Fatalf("integrity_check = %q, want ok", verdict)
	}
	events, err := copied.ListEvents(t.Context(), store.EventFilter{Limit: 10})
	if err != nil || len(events) == 0 {
		t.Fatalf("archived database has no events: %v (%d)", err, len(events))
	}
}

// TestBackupWhileTheDatabaseIsBeingWritten is why the daemon takes the backup
// rather than a second process copying the file: VACUUM INTO runs in a read
// transaction on the daemon's own connection, so committed rows cannot be
// half-copied however busy the store is.
func TestBackupWhileTheDatabaseIsBeingWritten(t *testing.T) {
	h := newBackupHarness(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := h.st.AppendEvent(t.Context(),
				&store.Event{Type: store.EventDaemonShuttingDown}); err != nil {
				return
			}
		}
	}()

	dst, _ := h.take(t, "busy.tar.gz")
	close(stop)
	wg.Wait()

	into := backup.Dirs{Data: t.TempDir(), Config: t.TempDir()}
	if _, err := backup.Restore(dst, into, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	copied, err := store.Open(filepath.Join(into.Data, backup.DatabaseEntry))
	if err != nil {
		t.Fatalf("open the archived database: %v", err)
	}
	defer func() { _ = copied.Close() }()
	verdict, err := copied.IntegrityCheck(t.Context())
	if err != nil || verdict != "ok" {
		t.Fatalf("integrity_check = %q (%v), want ok", verdict, err)
	}
}

func TestBackupRefusals(t *testing.T) {
	h := newBackupHarness(t)
	taken := filepath.Join(h.inbox, "taken.tar.gz")
	writeFile(t, taken, "somebody else's backup")

	for _, tc := range []struct {
		name string
		path string
	}{
		{"empty path", ""},
		{"relative path", filepath.Join("relative", "backup.tar.gz")},
		{"destination exists", taken},
		{"inside the transcript tree", filepath.Join(h.dirs.Data, "transcripts", "backup.tar.gz")},
		{"parent directory missing", filepath.Join(h.inbox, "nope", "backup.tar.gz")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := h.post(t, tc.path)
			wantError(t, resp, raw, http.StatusBadRequest, CodeValidationFailed)
		})
	}

	// The refused run left nothing behind, staging directory included.
	entries, err := os.ReadDir(h.inbox)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "taken.tar.gz" {
		t.Errorf("inbox = %v, want only the pre-existing file", names(entries))
	}
	body, err := os.ReadFile(taken)
	if err != nil || string(body) != "somebody else's backup" {
		t.Errorf("the pre-existing file was touched: %q (%v)", body, err)
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestBackupLeavesNoStagingDirectory: the VACUUM INTO copy is staged beside
// the destination, and a user who lists that directory afterwards must see
// one file.
func TestBackupLeavesNoStagingDirectory(t *testing.T) {
	h := newBackupHarness(t)
	h.take(t, "backup.tar.gz")
	entries, err := os.ReadDir(h.inbox)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("inbox = %v, want only the archive", names(entries))
	}
}

func TestBackupWithoutAStore(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	body := fmt.Appendf(nil, `{"path":%q}`, filepath.Join(t.TempDir(), "backup.tar.gz"))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/v1/daemon/backup", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	wantError(t, resp, raw, http.StatusInternalServerError, CodeInternal)
}

func TestBackupRejectsAWrongMethod(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/daemon/backup", testToken)
	wantError(t, resp, body, http.StatusMethodNotAllowed, CodeMethodNotAllowed)
}
