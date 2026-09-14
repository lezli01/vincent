//go:build !windows

package trigger

import (
	"os"
	"testing"
)

// An applied proposal is owner-only whatever the staged file's mode was and
// whatever the process umask allows, new file or replaced one (decision 20):
// a poll argv may carry a token.
func TestApplyProposalWritesOwnerOnly(t *testing.T) {
	const project = 7
	f := newProposalFixture(t)
	v := f.onDisk(t, proposalDoc("t", project, false, "", ""))
	path, _ := f.w.Path("t")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	// Chmod does not change the version token (it hashes mtime and content),
	// so the recorded version still matches.
	f.stage(t,
		map[string]string{
			"t":   proposalDoc("t", project, false, OnFirePropose, ""),
			"new": proposalDoc("new", project, false, "", ""),
		},
		map[string]string{"t": v, "new": Absent},
	)
	for _, name := range []string{"t.yaml", "new.yaml"} {
		if err := os.Chmod(f.staging+"/"+name, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	written, refusals, err := ApplyProposal(f.staging, f.w, project)
	if err != nil || len(refusals) > 0 {
		t.Fatalf("ApplyProposal = %v, %v", refusals, err)
	}
	for _, p := range written {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 600", p, mode)
		}
	}
	if len(written) != 2 {
		t.Errorf("written = %v, want both files", written)
	}
}
