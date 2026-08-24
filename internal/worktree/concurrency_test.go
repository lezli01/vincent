package worktree

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestConcurrentCreateInOneProject pins the invariant behind issue #126: several
// tasks admitted at the same moment in one project must each get their worktree
// (spec §10), not a git_error (§18) that has nothing to do with the task.
//
// create is not safe to run twice at once against one repository. It mutates
// state git does not lock for it: the `git worktree prune` it runs first (the
// prune-then-fail decision, T1.5/T1.6) deletes a peer's `.git/worktrees/{id}`
// in the window between that peer's mkdir and the `locked` file that would
// have protected it, and `git worktree add` enumerates `.git/worktrees/*`
// itself, so it also dies reading a peer that has a `gitdir` but not yet a
// `commondir`. CreateAndClaim takes only claims.RLock (task 005's reclaim
// lock), which permits exactly this overlap.
//
// A `fan_out` step (§7.6) spawns every lane at once into one repository, which
// turns this from rare into routine — and a lane blocked on git_error is not
// settled, so the parent sits in awaiting_children until a human looks.
//
// core.fsync=all is the amplifier, not the cause: it widens git's own
// half-built window from microseconds to milliseconds so the race that CI hits
// once in dozens of runs is reached in a couple of rounds. Sixteen lanes stand
// in for a wide fan-out.
func TestConcurrentCreateInOneProject(t *testing.T) {
	const (
		lanes  = 16
		rounds = 6
	)
	for round := range rounds {
		repo := testrepo.Init(t, "main")
		testrepo.Run(t, repo, "config", "core.fsync", "all")
		testrepo.Run(t, repo, "config", "core.fsyncMethod", "fsync")
		m := NewManager(gitx.New(), t.TempDir())

		errs := make([]error, lanes)
		gate := make(chan struct{})
		var wg sync.WaitGroup
		for i := range lanes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				id := int64(round*100 + i + 1)
				_, errs[i] = m.CreateAndClaim(t.Context(), repo, id,
					fmt.Sprintf("vincent/%d-lane", id), "main", nil)
			}()
		}
		close(gate)
		wg.Wait()

		for i, err := range errs {
			if err == nil {
				continue
			}
			// Name the reason so a future unrelated failure here is not
			// mistaken for this one.
			t.Fatalf("round %d lane %d: reason %q: %v\n"+
				"concurrent create in one project must not fail; git_error here means\n"+
				"one lane's prune or add tripped over another's half-built worktree",
				round, i, ReasonOf(err), strings.TrimSpace(err.Error()))
		}
	}
}
