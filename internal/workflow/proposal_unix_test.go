//go:build unix

package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

// apply writes through WriteFile (task 123 decision 5): a replaced global file
// keeps the mode it had, and a new one is created 0644 like any file the
// write endpoints create.
func TestApplyProposalModes(t *testing.T) {
	f := newProposalFixture(t)
	alpha := filepath.Join(f.global, "alpha.yaml")
	if err := os.Chmod(alpha, 0o640); err != nil {
		t.Fatal(err)
	}
	f.stage(t, map[string]string{"alpha.yaml": wfSource("alpha", "rewritten"), "gamma.yaml": wfSource("gamma", "new")},
		map[string]string{"alpha.yaml": f.version(t, "alpha.yaml"), "gamma.yaml": ProposalAbsent})
	if _, refusals, err := ApplyProposal(f.staging, f.global, Options{}, false); err != nil || refusals != nil {
		t.Fatalf("refusals = %v, err = %v", refusals, err)
	}
	for path, want := range map[string]os.FileMode{alpha: 0o640, filepath.Join(f.global, "gamma.yaml"): FilePerm} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", filepath.Base(path), got, want)
		}
	}
}
