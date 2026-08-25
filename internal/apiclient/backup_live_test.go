package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// newBackupClient wires the client to the real backup handler over a real
// store and data dir. A stub would prove nothing here: the endpoint's answer
// is derived from bytes it wrote to disk, and the wire types are exactly what
// this test exists to hold together.
func newBackupClient(t *testing.T) *apiclient.Client {
	t.Helper()
	dirs := config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
	st, err := store.Open(filepath.Join(dirs.Data, backup.DatabaseEntry))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	transcript := filepath.Join(dirs.Data, backup.TranscriptsPrefix, "1", "0-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	if err := os.WriteFile(transcript, []byte(`{"type":"start"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirs.Config, "config.yaml"),
		[]byte("listen: \"127.0.0.1:0\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		Dirs:        dirs,
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return apiclient.New(ts.URL, testToken)
}

func TestBackupOverTheWire(t *testing.T) {
	c := newBackupClient(t)
	dst := filepath.Join(t.TempDir(), "backup.tar.gz")

	res, err := c.Backup(t.Context(), dst)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if res.Path != dst {
		t.Errorf("Path = %q, want %q", res.Path, dst)
	}
	if res.Bytes <= 0 || res.DatabaseBytes <= 0 || res.TranscriptBytes <= 0 {
		t.Errorf("result = %+v, want every size filled in", res)
	}
	if res.SchemaVersion != store.NewestMigration() {
		t.Errorf("SchemaVersion = %d, want %d", res.SchemaVersion, store.NewestMigration())
	}
	if res.CreatedAt == "" {
		t.Error("CreatedAt is empty")
	}

	// The manifest the daemon embedded says the same things the response did:
	// two encodings of one fact, and the pair only stays honest if something
	// compares them.
	m, err := backup.ReadManifest(dst)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.SchemaVersion != res.SchemaVersion || m.CreatedAt != res.CreatedAt {
		t.Errorf("manifest = %+v, response = %+v", m, res)
	}
	if m.VincentVersion == "" {
		t.Error("manifest carries no vincent version")
	}
}

// TestBackupErrorOverTheWire: a refusal arrives as the §13.1 envelope, so the
// CLI can print the daemon's own words rather than a transport error.
func TestBackupErrorOverTheWire(t *testing.T) {
	c := newBackupClient(t)
	dst := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := os.WriteFile(dst, []byte("taken"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := c.Backup(t.Context(), dst)
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("Backup over an existing file = %v, want *apiclient.Error", err)
	}
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != "validation_failed" {
		t.Errorf("error = %+v, want a 400 validation_failed", apiErr)
	}
	if apiErr.Message == "" {
		t.Error("error carries no message for the CLI to print")
	}
}
