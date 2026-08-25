package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

func joinRows(rows [][]string) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t"))
		b.WriteString("\n")
	}
	return b.String()
}

// TestDatabaseRowsUnknownWithoutADaemon: an unknown database prints one row
// saying so. Rendering zeros would be worse than printing nothing — "0 rows,
// 0 bytes" is a claim about a database this process never opened.
func TestDatabaseRowsUnknownWithoutADaemon(t *testing.T) {
	out := joinRows(doctorDatabaseRows(apiclient.DoctorDatabase{Path: "/data/vincent.db"}))
	if !strings.Contains(out, "unknown — daemon not running") {
		t.Errorf("the unknown database does not say so:\n%s", out)
	}
	for _, forbidden := range []string{"rows", "oldest event", "total on disk", "0B"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("an unknown database rendered %q as if it had been read:\n%s", forbidden, out)
		}
	}
}

func TestDatabaseRowsRenderTheFootprintCountsAndSpan(t *testing.T) {
	oldest := time.Now().Add(-72 * time.Hour)
	out := joinRows(doctorDatabaseRows(apiclient.DoctorDatabase{
		Path:            "/data/vincent.db",
		Known:           true,
		SizeBytes:       4 << 20,
		WALBytes:        1 << 20,
		SHMBytes:        32 << 10,
		TotalBytes:      4<<20 + 1<<20 + 32<<10,
		SchemaVersion:   12,
		NewestMigration: 12,
		IntegrityCheck:  "ok",
		TableRows: map[string]int64{
			"events": 91234, "step_runs": 812, "tasks": 140, "projects": 7,
		},
		OldestEventAt:         &oldest,
		WorkflowSnapshotBytes: 2 << 20,
	}))
	for _, want := range []string{
		"5.0MB",     // the total, which is what WAL makes worth reporting
		"wal 1.0MB", // named rather than folded in silently
		"events 91234",
		"projects 7",
		"2.0MB", // the workflow-snapshot total
		"3 days",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the database group does not show %q:\n%s", want, out)
		}
	}
	// Biggest table first: the point of the row is to name what is growing.
	if i, j := strings.Index(out, "events 91234"), strings.Index(out, "projects 7"); i > j {
		t.Errorf("the row counts are not ordered biggest-first:\n%s", out)
	}
}

// TestDatabaseSpanWithNoEvents: a fresh install has no span, which is stated
// rather than rendered as the zero time.
func TestDatabaseSpanWithNoEvents(t *testing.T) {
	out := joinRows(doctorDatabaseRows(apiclient.DoctorDatabase{
		Path: "/data/vincent.db", Known: true, SizeBytes: 4096, TotalBytes: 4096,
	}))
	if !strings.Contains(out, "none yet") {
		t.Errorf("an install with no events does not say so:\n%s", out)
	}
	if strings.Contains(out, "0001-01-01") {
		t.Errorf("the zero time reached the report:\n%s", out)
	}
}
