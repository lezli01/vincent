package chatrun

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/gitx"
)

// TestEveryTurnIsCheckpointed is task 167.2's acceptance on all four
// dialects: each turn writes refs/vincent/chats/<id>/turn-<seq> and records
// its SHA and the HEAD under it on the turn row, and the snapshot holds the
// worktree's uncommitted state as it was when the turn began.
func TestEveryTurnIsCheckpointed(t *testing.T) {
	for _, agentName := range []string{"claude", "codex", "cursor", "copilot"} {
		t.Run(agentName, func(t *testing.T) {
			h := newHarness(t)
			c := h.chatOn(t, agentName)
			git := gitx.New()
			ctx := t.Context()
			if err := os.WriteFile(filepath.Join(c.WorktreePath, "draft.txt"), []byte("wip\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			head, err := git.Run(ctx, c.WorktreePath, "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			statusBefore, _ := git.Run(ctx, c.WorktreePath, "status", "--porcelain")

			for seq := 1; seq <= 2; seq++ {
				turn := h.sendAndWait(t, c.ID, "hello")
				h.waitIdle(t, c.ID)
				if turn.State != chatstate.TurnDone {
					t.Fatalf("turn %d = %s (%s: %s)", seq, turn.State, turn.FailReason, turn.ErrorMessage)
				}
				if turn.CheckpointSHA == "" {
					t.Fatalf("turn %d recorded no checkpoint", seq)
				}
				if turn.HeadBefore != head || turn.HeadAfter != head {
					t.Errorf("turn %d heads = %q/%q, want %q", seq, turn.HeadBefore, turn.HeadAfter, head)
				}
				ref, err := git.Run(ctx, h.repo, "rev-parse", CheckpointRef(c.ID, seq))
				if err != nil {
					t.Fatalf("checkpoint ref from the main checkout: %v", err)
				}
				if ref != turn.CheckpointSHA {
					t.Errorf("ref = %s, row = %s", ref, turn.CheckpointSHA)
				}
				if blob, err := git.Run(ctx, h.repo, "show", turn.CheckpointSHA+":draft.txt"); err != nil || blob != "wip" {
					t.Errorf("checkpoint draft.txt = %q, %v", blob, err)
				}
			}
			// The checkpoint touched neither the index nor the files.
			if after, _ := git.Run(ctx, c.WorktreePath, "status", "--porcelain"); after != statusBefore {
				t.Errorf("status changed: %q -> %q", statusBefore, after)
			}
		})
	}
}

// TestCheckpointFailureNeverSpawns: a turn whose checkpoint cannot be taken
// fails checkpoint_failed and no agent process is started — a turn never
// runs unprotected.
func TestCheckpointFailureNeverSpawns(t *testing.T) {
	h := newHarness(t)
	c := h.chat(t)
	// Not a repository: the snapshot cannot resolve HEAD.
	if _, err := h.store.SetChatWorktree(t.Context(), c.ID, t.TempDir(), "deadbeef"); err != nil {
		t.Fatal(err)
	}
	turn := h.sendAndWait(t, c.ID, "hello")
	if turn.State != chatstate.TurnFailed || turn.FailReason != ReasonCheckpointFailed {
		t.Fatalf("turn = %s (%s: %s), want failed checkpoint_failed", turn.State, turn.FailReason, turn.ErrorMessage)
	}
	if turn.PID != nil || turn.ExitCode != nil {
		t.Errorf("an agent ran: pid %v exit %v", turn.PID, turn.ExitCode)
	}
	if turn.CheckpointSHA != "" {
		t.Errorf("checkpoint recorded on a failed checkpoint: %s", turn.CheckpointSHA)
	}
}

// TestUnreadableFileDoesNotStopTheChat is review F1: an untracked file git
// cannot read is left out of the checkpoint, and the turn runs anyway — every
// later turn too — with the skipped path recorded in its transcript.
func TestUnreadableFileDoesNotStopTheChat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod cannot make a file unreadable on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file")
	}
	h := newHarness(t)
	c := h.chat(t)
	secret := filepath.Join(c.WorktreePath, "secret")
	if err := os.WriteFile(secret, []byte("root's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secret, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o600) })

	for seq := 1; seq <= 2; seq++ {
		turn := h.sendAndWait(t, c.ID, "hello")
		h.waitIdle(t, c.ID)
		if turn.State != chatstate.TurnDone {
			t.Fatalf("turn %d = %s (%s: %s)", seq, turn.State, turn.FailReason, turn.ErrorMessage)
		}
		if turn.CheckpointSHA == "" {
			t.Fatalf("turn %d recorded no checkpoint", seq)
		}
		if body := h.transcript(t, c.ID, seq); !strings.Contains(body, `"type":"vincent.checkpoint_skipped"`) ||
			!strings.Contains(body, `"secret"`) {
			t.Errorf("turn %d transcript does not name the skipped file:\n%s", seq, body)
		}
	}
}
