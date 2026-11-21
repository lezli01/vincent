package worktree

import (
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// CreateAtAndClaim cuts a branch at a commit behind the base's tip (task 167
// decision 11): the fork's worktree starts where the source turn started.
func TestCreateAtCutsFromTheCommit(t *testing.T) {
	m, _, local := pullRepos(t)
	start := testrepo.Run(t, local, "rev-parse", "HEAD")
	testrepo.Run(t, local, "commit", "-q", "--allow-empty", "-m", "later")

	claimed := ""
	c, err := m.CreateAtAndClaim(t.Context(), local, ChatOwner(9), "vincent/9-fork", start,
		func(c Created) error { claimed = c.Path; return nil })
	if err != nil {
		t.Fatalf("CreateAtAndClaim: %v", err)
	}
	if claimed != c.Path {
		t.Errorf("claim saw %q, want %q", claimed, c.Path)
	}
	if got := testrepo.Run(t, c.Path, "rev-parse", "HEAD"); got != start {
		t.Errorf("HEAD = %s, want the start point %s", got, start)
	}
	if c.BaseSHA != "" || c.Fetch.Result != FetchDisabled {
		t.Errorf("Created = %+v, want no base and no fetch", c)
	}
	if _, err := m.CreateAtAndClaim(t.Context(), local, ChatOwner(10), "vincent/9-fork", start, nil); err == nil {
		t.Error("an existing branch was reused")
	}
	if _, err := m.CreateAtAndClaim(t.Context(), local, ChatOwner(11), "vincent/11-x",
		"0123456789012345678901234567890123456789", nil); err == nil {
		t.Error("a missing start point was accepted")
	}
}
