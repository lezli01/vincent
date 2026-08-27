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

// The GitHub integration row (task 035). Every "no" it can report leaves task
// creation without an issue working exactly as before, so the row states the
// facts and the consequence and never accuses — and none of it is a Problem,
// which is why `vincent doctor` does not exit 1 over any of these.

// TestDoctorGitHubRowWithoutGH is the diagnosis the daemon log used to carry
// alone: no `gh`, no token, and a sentence saying what still works.
func TestDoctorGitHubRowWithoutGH(t *testing.T) {
	out := joinRows(doctorGitHubRows(apiclient.DoctorGitHub{
		Enabled: true,
		Message: "no GitHub credential: gh is not installed or not authenticated, " +
			"and neither GITHUB_TOKEN nor GH_TOKEN is set",
	}))
	for _, want := range []string{
		"enabled\tyes",
		"gh cli\tnot found",
		"token\tnot set",
		"unavailable: no GitHub credential",
		"tasks can still be created without an issue",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the github row does not show %q:\n%s", want, out)
		}
	}
}

// TestDoctorGitHubRowLoggedOut: an installed `gh` that is not logged in is the
// case worth naming — it probes as present and then answers nothing.
func TestDoctorGitHubRowLoggedOut(t *testing.T) {
	row := apiclient.DoctorGitHub{Enabled: true, Message: "no GitHub credential"}
	row.GHFound, row.GHPath, row.GHVersion = true, "/usr/bin/gh", "gh version 2.98.0 (2026-08-20)"
	out := joinRows(doctorGitHubRows(row))
	if !strings.Contains(out, "gh version 2.98.0") || !strings.Contains(out, "auth no") {
		t.Errorf("a logged-out gh is not reported as found-but-unauthenticated:\n%s", out)
	}
}

// TestDoctorGitHubRowNamesTheVariableNotTheToken: a diagnostic pastes into an
// issue, so the token's *name* is reported and its value never is.
func TestDoctorGitHubRowNamesTheVariableNotTheToken(t *testing.T) {
	row := apiclient.DoctorGitHub{Enabled: true, Usable: true}
	row.TokenVar, row.Via = "GH_TOKEN", "token"
	out := joinRows(doctorGitHubRows(row))
	if !strings.Contains(out, "set (GH_TOKEN)") {
		t.Errorf("the token row does not name the variable:\n%s", out)
	}
	if !strings.Contains(out, "readable via token") {
		t.Errorf("a usable integration does not say so:\n%s", out)
	}
}

// TestDoctorGitHubRowDisabled: the toggle off is not a fault, and the row says
// so plainly rather than reporting an unavailability.
func TestDoctorGitHubRowDisabled(t *testing.T) {
	out := joinRows(doctorGitHubRows(apiclient.DoctorGitHub{Enabled: false}))
	if !strings.Contains(out, "enabled\tno") {
		t.Errorf("a disabled integration does not say so:\n%s", out)
	}
	if !strings.Contains(out, "not read (integration disabled)") {
		t.Errorf("the issues row does not explain the disabled state:\n%s", out)
	}
	if strings.Contains(out, "unavailable") {
		t.Errorf("a disabled integration is reported as unavailable:\n%s", out)
	}
}
