package worktree

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/testrepo"
)

// aheadBehindGit reports whether this git has for-each-ref's ahead-behind atom.
func aheadBehindGit(t *testing.T) bool {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "version").Output()
	if err != nil {
		return false
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 {
		return false
	}
	parts := strings.Split(fields[2], ".")
	if len(parts) < 2 {
		return false
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	return major > 2 || (major == 2 && minor >= 41)
}

func TestBranchesAheadCountsCommitsPastTheBase(t *testing.T) {
	if !aheadBehindGit(t) {
		t.Skip("this git has no ahead-behind atom")
	}
	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "checkout", "-b", "fix")
	for _, msg := range []string{"one", "two", "three"} {
		testrepo.Run(t, repo, "commit", "--allow-empty", "-m", msg)
	}
	testrepo.Run(t, repo, "checkout", "main")

	m := &Manager{git: gitx.New()}
	got := m.BranchesAhead(t.Context(), repo, "main")
	if got["fix"] != 3 {
		t.Errorf("fix is %d ahead, want 3: %v", got["fix"], got)
	}
	if n, ok := got["main"]; !ok || n != 0 {
		t.Errorf("main is %d ahead (listed %v), want 0", n, ok)
	}
}

// A base that does not exist leaves the picker with nothing to say, not an error.
func TestBranchesAheadWithoutABaseSaysNothing(t *testing.T) {
	repo := testrepo.Init(t, "main")
	m := &Manager{git: gitx.New()}
	for _, base := range []string{"", "no-such-branch"} {
		if got := m.BranchesAhead(t.Context(), repo, base); len(got) != 0 {
			t.Errorf("base %q answered %v", base, got)
		}
	}
}
