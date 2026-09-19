package taskrun

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/trigger"
	"github.com/lezli01/vincent/internal/workflow"
)

// Deleting a task removes its trigger and workflow proposals (task 098
// decision 5, task 123 decision 4), and
// only that: another task's proposal, the task's transcripts and anything
// outside the data dir stay.
func TestRemoveProposalDirStaysInsideItsOwnDirectory(t *testing.T) {
	data := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mine := trigger.ProposalDir(data, 12)
	mineWorkflows := workflow.ProposalDir(data, 12)
	other := trigger.ProposalDir(data, 13)
	otherWorkflows := workflow.ProposalDir(data, 13)
	transcripts := filepath.Join(data, "transcripts", "12")
	for _, dir := range []string{mine, mineWorkflows, other, otherWorkflows, transcripts} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, trigger.ManifestName), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	RemoveProposalDir(data, 12, log)
	// Both kinds: a global update-workflows run stages beside the trigger
	// built-ins (task 123 decision 4).
	for _, dir := range []string{mine, mineWorkflows} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("task 12's proposal %s survived its delete: %v", dir, err)
		}
	}
	for _, dir := range []string{other, otherWorkflows, transcripts} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s was removed with task 12's proposal: %v", dir, err)
		}
	}

	// Already gone, no data dir and a non-positive id are all quiet no-ops;
	// the last two must never reach a relative or root-level path.
	RemoveProposalDir(data, 12, log)
	RemoveProposalDir("", 13, log)
	RemoveProposalDir(data, 0, log)
	RemoveProposalDir(data, -13, log)
	for _, dir := range []string{other, otherWorkflows} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("a no-op call removed task 13's proposal %s: %v", dir, err)
		}
	}
}
