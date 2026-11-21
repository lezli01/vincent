package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatnote"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// The chat diff routes (task 169) over the real handlers, a real store and
// real git. Most cases write their chat and turn rows straight into the
// store, as the export's do, so no agent runs; the live-turn case runs the
// fake agent through the real runner.

// diffLines is the tracked file every worktree below changes: thirty lines,
// so three edits far enough apart are three hunks.
func diffLines(edit map[int]string) string {
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		if v, ok := edit[i]; ok {
			b.WriteString(v + "\n")
			continue
		}
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// diffWorktree is a worktree of the harness repo, one commit past main with
// line 2 of long.txt changed, line 25 changed and uncommitted, and an
// untracked new.txt.
func (h *exportHarness) diffWorktree(t *testing.T, name string) string {
	t.Helper()
	// Whatever the host's git would do to line endings, these files hold LF.
	testrepo.Run(t, h.repo, "config", "core.autocrlf", "false")
	if _, err := os.Stat(filepath.Join(h.repo, "long.txt")); err != nil {
		testrepo.WriteFile(t, h.repo, "long.txt", diffLines(nil))
		testrepo.Run(t, h.repo, "add", "long.txt")
		testrepo.Run(t, h.repo, "commit", "-q", "-m", "long")
	}
	dir := filepath.Join(t.TempDir(), name)
	testrepo.Run(t, h.repo, "worktree", "add", "-q", "-b", name, dir, "main")
	testrepo.WriteFile(t, dir, "long.txt", diffLines(map[int]string{2: "line 2 committed"}))
	testrepo.Run(t, dir, "commit", "-q", "-am", "a commit")
	testrepo.WriteFile(t, dir, "long.txt", diffLines(map[int]string{2: "line 2 committed", 25: "line 25 dirty"}))
	testrepo.WriteFile(t, dir, "new.txt", "brand new\n")
	return dir
}

// checkpointTurn records a finished turn on c whose checkpoint is dir as it
// stands now, exactly as the runner takes one before an agent starts.
func (h *exportHarness) checkpointTurn(t *testing.T, c *store.Chat, dir string) {
	t.Helper()
	snap, err := gitx.New().Snapshot(t.Context(), dir, chatrun.CheckpointScratch(h.dataDir), "checkpoint")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	h.turn(t, c, "go", func(ct *store.ChatTurn) { ct.CheckpointSHA = snap.Commit }, "")
}

func (h *exportHarness) chatDiff(t *testing.T, id int64, scope string) chatDiffBody {
	t.Helper()
	path := fmt.Sprintf("/v1/chats/%d/diff", id)
	if scope != "" {
		path += "?scope=" + scope
	}
	resp, body := h.doJSON(t, http.MethodGet, path, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
	}
	var d chatDiffBody
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("decode diff: %v (%s)", err, body)
	}
	return d
}

// hunkWith finds the one hunk of path whose body contains text.
func hunkWith(t *testing.T, d chatDiffBody, path, text string) chatDiffHunkBody {
	t.Helper()
	for _, f := range d.Files {
		if f.Path != path {
			continue
		}
		for _, hk := range f.Hunks {
			if strings.Contains(hk.Body, text) {
				return hk
			}
		}
	}
	t.Fatalf("no hunk of %s contains %q in %+v", path, text, d)
	return chatDiffHunkBody{}
}

func diffPaths(d chatDiffBody) string {
	var paths []string
	for _, f := range d.Files {
		paths = append(paths, f.Path)
	}
	return strings.Join(paths, ",")
}

// hunkCall posts a reject, accept or unaccept and returns the status, the
// error code (empty on success) and the raw body.
func (h *projectHarness) hunkCall(t *testing.T, id int64, verb string, body map[string]any) (int, string, []byte) {
	t.Helper()
	resp, raw := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/diff/%s", id, verb), body)
	var env map[string]any
	_ = json.Unmarshal(raw, &env)
	return resp.StatusCode, errCode(env), raw
}

func (h *exportHarness) reject(t *testing.T, id int64, scope, path, hash string) chatHunkRejectBody {
	t.Helper()
	status, _, raw := h.hunkCall(t, id, "reject", map[string]any{"scope": scope, "path": path, "hunk": hash})
	if status != http.StatusOK {
		t.Fatalf("reject %s = %d %s", path, status, raw)
	}
	var out chatHunkRejectBody
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode reject: %v (%s)", err, raw)
	}
	return out
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestChatDiffScopes(t *testing.T) {
	h := newExportHarness(t)
	dir := h.diffWorktree(t, "scopes")
	c := h.chat(t, &store.Chat{WorktreePath: dir})
	mergeBase := testrepo.Run(t, dir, "merge-base", "main", "HEAD")

	// No turn yet: the turn scope has no start, and says so.
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/diff?scope=turn", c.ID), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), CodeChatDiffUnavailable) {
		t.Fatalf("turn scope with no turn = %d %s", resp.StatusCode, body)
	}

	// Base: the commit, the dirty line and the untracked file. Omitted scope
	// is base.
	for _, scope := range []string{"", "base"} {
		d := h.chatDiff(t, c.ID, scope)
		if d.Scope != "base" || d.From != strings.TrimSpace(mergeBase) {
			t.Errorf("scope %q: scope=%q from=%q, want base from %s", scope, d.Scope, d.From, mergeBase)
		}
		if got := diffPaths(d); got != "long.txt,new.txt" {
			t.Fatalf("base files = %s", got)
		}
		long, added := d.Files[0], d.Files[1]
		if long.Status != "modified" || len(long.Hunks) != 2 || !long.Rejectable || long.Added != 2 || long.Removed != 2 {
			t.Errorf("long.txt = %+v", long)
		}
		if added.Status != "added" || len(added.Hunks) != 1 || added.Hunks[0].Body != "+brand new" {
			t.Errorf("untracked new.txt = %+v", added)
		}
		if d.Added != 3 || d.Removed != 2 {
			t.Errorf("totals = +%d -%d, want +3 -2", d.Added, d.Removed)
		}
		hk := hunkWith(t, d, "long.txt", "+line 25 dirty")
		if len(hk.Hash) != 64 || !strings.HasPrefix(hk.Header, "@@ -") || hk.Accepted ||
			strings.HasSuffix(hk.Body, "\n") || !strings.Contains(hk.Body, "\n-line 25\n+line 25 dirty\n") {
			t.Errorf("hunk = %+v", hk)
		}
	}

	// A turn whose snapshot failed carries no checkpoint: still unavailable,
	// never the base's diff under another name.
	h.turn(t, c, "first", func(*store.ChatTurn) {}, "")
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/diff?scope=turn", c.ID), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), CodeChatDiffUnavailable) ||
		!strings.Contains(string(body), "no checkpoint") {
		t.Fatalf("turn scope with no checkpoint = %d %s", resp.StatusCode, body)
	}

	// Turn: only what changed since the latest turn's checkpoint, untracked
	// files included.
	h.checkpointTurn(t, c, dir)
	if d := h.chatDiff(t, c.ID, "turn"); len(d.Files) != 0 {
		t.Fatalf("turn diff straight after the checkpoint = %+v", d)
	}
	testrepo.WriteFile(t, dir, "long.txt",
		diffLines(map[int]string{2: "line 2 committed", 14: "line 14 turn", 25: "line 25 dirty"}))
	testrepo.WriteFile(t, dir, "turn.txt", "from the turn\n")
	d := h.chatDiff(t, c.ID, "turn")
	if got := diffPaths(d); got != "long.txt,turn.txt" || d.Scope != "turn" || len(d.From) != 40 {
		t.Fatalf("turn diff = %s scope %q from %q", got, d.Scope, d.From)
	}
	if len(d.Files[0].Hunks) != 1 || !strings.Contains(d.Files[0].Hunks[0].Body, "+line 14 turn") {
		t.Errorf("turn long.txt = %+v", d.Files[0])
	}
	if base := h.chatDiff(t, c.ID, "base"); len(base.Files) != 3 || len(base.Files[0].Hunks) != 3 {
		t.Errorf("base after the turn = %+v", base)
	}

	// The stat route is the base scope's totals.
	resp, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/diff/stat", c.ID), nil)
	var st chatDiffStatBody
	if err := json.Unmarshal(body, &st); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("stat = %d %s (%v)", resp.StatusCode, body, err)
	}
	if st != (chatDiffStatBody{Files: 3, Added: 5, Removed: 3}) {
		t.Errorf("stat = %+v", st)
	}

	// An unknown scope is a 400, on the read and on both writes.
	resp, _ = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/diff?scope=all", c.ID), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("scope=all = %d, want 400", resp.StatusCode)
	}
	for _, verb := range []string{"reject", "accept"} {
		if status, _, raw := h.hunkCall(t, c.ID, verb, map[string]any{"scope": "all", "path": "x", "hunk": "y"}); status != http.StatusBadRequest {
			t.Errorf("%s scope=all = %d %s, want 400", verb, status, raw)
		}
	}
	// Neither read moved the index or HEAD, or left anything in the worktree.
	if staged := testrepo.Run(t, dir, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Errorf("the index changed: %q", staged)
	}
}

// TestChatDiffEmptyListsAreArrays: a clean worktree answers `[]`, never null.
func TestChatDiffEmptyListsAreArrays(t *testing.T) {
	h := newExportHarness(t)
	dir := filepath.Join(t.TempDir(), "clean")
	testrepo.Run(t, h.repo, "worktree", "add", "-q", "-b", "clean", dir, "main")
	c := h.chat(t, &store.Chat{WorktreePath: dir})
	_, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/diff", c.ID), nil)
	if !strings.Contains(string(body), `"files":[]`) {
		t.Errorf("clean diff = %s", body)
	}
	testrepo.WriteFile(t, dir, "bin.dat", "a\x00b\x00")
	_, body = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/diff", c.ID), nil)
	if !strings.Contains(string(body), `"hunks":[]`) || !strings.Contains(string(body), `"binary":true`) {
		t.Errorf("binary diff = %s", body)
	}
}

func TestChatHunkRejectRevertsOneHunkAndNotesIt(t *testing.T) {
	h := newExportHarness(t)
	dir := h.diffWorktree(t, "reject")
	c := h.chat(t, &store.Chat{WorktreePath: dir})
	head := testrepo.Run(t, dir, "rev-parse", "HEAD")
	testrepo.Run(t, dir, "add", "new.txt") // something staged, to prove the index is left alone
	staged := testrepo.Run(t, dir, "diff", "--cached")

	d := h.chatDiff(t, c.ID, "base")
	dirty := hunkWith(t, d, "long.txt", "+line 25 dirty")
	committed := hunkWith(t, d, "long.txt", "+line 2 committed")

	// An accepted mark on the hunk goes with the reject.
	if status, _, raw := h.hunkCall(t, c.ID, "accept", map[string]any{"scope": "base", "path": "long.txt", "hunk": dirty.Hash}); status != http.StatusNoContent {
		t.Fatalf("accept = %d %s", status, raw)
	}
	out := h.reject(t, c.ID, "base", "long.txt", dirty.Hash)
	wantNote := chatnote.Heading + "\n- long.txt " + dirty.Header + "\n\n"
	if !out.NoteAdded || out.Warning != "" || out.Draft != wantNote {
		t.Fatalf("reject = %+v, want draft %q", out, wantNote)
	}
	// Exactly that hunk is gone from disk; the index and HEAD are as they were.
	if got := readFile(t, dir, "long.txt"); got != diffLines(map[int]string{2: "line 2 committed"}) {
		t.Errorf("long.txt after the reject:\n%s", got)
	}
	if got := testrepo.Run(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved: %s -> %s", head, got)
	}
	if got := testrepo.Run(t, dir, "diff", "--cached"); got != staged {
		t.Errorf("the index changed:\n%s", got)
	}
	if marks, _ := h.store.ListChatHunkReviews(t.Context(), c.ID); len(marks) != 0 {
		t.Errorf("the rejected hunk kept its mark: %+v", marks)
	}
	stored, err := h.store.GetChat(t.Context(), c.ID)
	if err != nil || stored.Draft != wantNote {
		t.Fatalf("stored draft = %q, %v", stored.Draft, err)
	}

	// The hash is gone from the diff now: rejecting it again is stale, as is
	// a path the diff does not hold.
	for _, body := range []map[string]any{
		{"scope": "base", "path": "long.txt", "hunk": dirty.Hash},
		{"scope": "base", "path": "nowhere.txt", "hunk": dirty.Hash},
	} {
		if status, code, raw := h.hunkCall(t, c.ID, "reject", body); status != http.StatusConflict || code != CodeChatHunkStale {
			t.Errorf("stale reject %v = %d %s", body, status, raw)
		}
	}

	// A second reject extends the same block, above what the user typed.
	typed := wantNote + "and please add a test"
	if resp, raw := h.doJSON(t, http.MethodPut, fmt.Sprintf("/v1/chats/%d/draft", c.ID), map[string]any{"draft": typed}); resp.StatusCode != http.StatusOK {
		t.Fatalf("put draft = %d %s", resp.StatusCode, raw)
	}
	out = h.reject(t, c.ID, "base", "long.txt", committed.Hash)
	want := chatnote.Heading + "\n- long.txt " + dirty.Header + "\n- long.txt " + committed.Header +
		"\n\nand please add a test"
	if out.Draft != want {
		t.Errorf("second reject draft = %q, want %q", out.Draft, want)
	}
	// The committed line is reverted in the working tree only: an
	// uncommitted edit, never history.
	if got := readFile(t, dir, "long.txt"); got != diffLines(nil) {
		t.Errorf("long.txt after the second reject:\n%s", got)
	}
	if got := testrepo.Run(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved: %s -> %s", head, got)
	}

	// A draft the user edited so it no longer starts with the block gets a
	// new block prepended. Rejecting an added file's only hunk deletes it.
	edited := "first, " + want
	if resp, raw := h.doJSON(t, http.MethodPut, fmt.Sprintf("/v1/chats/%d/draft", c.ID), map[string]any{"draft": edited}); resp.StatusCode != http.StatusOK {
		t.Fatalf("put draft = %d %s", resp.StatusCode, raw)
	}
	added := hunkWith(t, h.chatDiff(t, c.ID, "base"), "new.txt", "+brand new")
	out = h.reject(t, c.ID, "base", "new.txt", added.Hash)
	if want := chatnote.Heading + "\n- new.txt " + added.Header + "\n\n" + edited; out.Draft != want {
		t.Errorf("third reject draft = %q, want %q", out.Draft, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("new.txt survived the reject of its only hunk: %v", err)
	}
}

func TestChatHunkRejectInTheTurnScope(t *testing.T) {
	h := newExportHarness(t)
	dir := h.diffWorktree(t, "turnreject")
	c := h.chat(t, &store.Chat{WorktreePath: dir})
	h.checkpointTurn(t, c, dir)
	before := readFile(t, dir, "long.txt")
	testrepo.WriteFile(t, dir, "long.txt",
		diffLines(map[int]string{2: "line 2 committed", 14: "line 14 turn", 25: "line 25 dirty"}))

	hk := hunkWith(t, h.chatDiff(t, c.ID, "turn"), "long.txt", "+line 14 turn")
	// A hunk of the base scope that the turn scope does not hold is stale there.
	base := hunkWith(t, h.chatDiff(t, c.ID, "base"), "long.txt", "+line 25 dirty")
	if status, code, raw := h.hunkCall(t, c.ID, "reject", map[string]any{"scope": "turn", "path": "long.txt", "hunk": base.Hash}); status != http.StatusConflict || code != CodeChatHunkStale {
		t.Fatalf("base hunk in the turn scope = %d %s", status, raw)
	}
	h.reject(t, c.ID, "turn", "long.txt", hk.Hash)
	if got := readFile(t, dir, "long.txt"); got != before {
		t.Errorf("long.txt after the turn-scope reject:\n%s", got)
	}
}

func TestChatHunkRejectConflictAndNotRejectable(t *testing.T) {
	h := newExportHarness(t)
	testrepo.Run(t, h.repo, "config", "core.autocrlf", "false")
	testrepo.WriteFile(t, h.repo, "gone.txt", "was here\n")
	testrepo.WriteFile(t, h.repo, "old.txt", "one\ntwo\nthree\nfour\nfive\n")
	testrepo.Run(t, h.repo, "add", ".")
	testrepo.Run(t, h.repo, "commit", "-q", "-m", "files")
	dir := filepath.Join(t.TempDir(), "conflict")
	testrepo.Run(t, h.repo, "worktree", "add", "-q", "-b", "conflict", dir, "main")
	// gone.txt is deleted in a commit and then ignored, and an ignored file
	// of that name sits on disk. The diff says "deleted" — the snapshot
	// leaves ignored files out — and reversing that would have to create a
	// file that is already there, which git refuses.
	testrepo.Run(t, dir, "rm", "-q", "gone.txt")
	testrepo.Run(t, dir, "mv", "old.txt", "renamed.txt")
	testrepo.WriteFile(t, dir, ".gitignore", "gone.txt\n")
	testrepo.Run(t, dir, "add", ".")
	testrepo.Run(t, dir, "commit", "-q", "-m", "delete, rename, ignore")
	testrepo.WriteFile(t, dir, "gone.txt", "something else entirely\n")
	testrepo.WriteFile(t, dir, "bin.dat", "a\x00b\x00")
	// A directory with its own .git, as an agent's `git clone` leaves one:
	// the snapshot records it as a gitlink (review F4).
	nested := filepath.Join(dir, "vendor", "lib")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	testrepo.Run(t, nested, "init", "-q")
	testrepo.Run(t, nested, "config", "user.name", "t")
	testrepo.Run(t, nested, "config", "user.email", "t@example.invalid")
	testrepo.Run(t, nested, "config", "commit.gpgsign", "false")
	testrepo.WriteFile(t, nested, "lib.go", "package lib\n")
	testrepo.Run(t, nested, "add", "-A")
	testrepo.Run(t, nested, "commit", "-q", "-m", "nested")
	c := h.chat(t, &store.Chat{WorktreePath: dir})

	d := h.chatDiff(t, c.ID, "base")
	gone := hunkWith(t, d, "gone.txt", "-was here")
	status, code, raw := h.hunkCall(t, c.ID, "reject", map[string]any{"scope": "base", "path": "gone.txt", "hunk": gone.Hash})
	if status != http.StatusConflict || code != CodeChatHunkConflict {
		t.Fatalf("conflicting reject = %d %s", status, raw)
	}
	if got := readFile(t, dir, "gone.txt"); got != "something else entirely\n" {
		t.Errorf("a refused reject wrote the file: %q", got)
	}
	why := map[string]string{"bin.dat": "binary", "renamed.txt": "renamed", "vendor/lib": "gitlink"}
	for _, f := range d.Files {
		want, ok := why[f.Path]
		if !ok {
			continue
		}
		delete(why, f.Path)
		if f.Rejectable || f.NotRejectable != want {
			t.Errorf("%s: rejectable %v, not_rejectable %q, want %q", f.Path, f.Rejectable, f.NotRejectable, want)
		}
		// The file's own hunk where it has one: the gitlink's "Subproject
		// commit" hunk is exactly what git would pretend to reverse.
		hash := gone.Hash
		if len(f.Hunks) > 0 {
			hash = f.Hunks[0].Hash
		}
		status, code, raw := h.hunkCall(t, c.ID, "reject", map[string]any{"scope": "base", "path": f.Path, "hunk": hash})
		if status != http.StatusConflict || code != CodeChatHunkNotRejectable {
			t.Errorf("reject of %s = %d %s", f.Path, status, raw)
		}
	}
	if len(why) != 0 {
		t.Errorf("not in the diff: %v", why)
	}
	if got := readFile(t, dir, "vendor/lib/lib.go"); got != "package lib\n" {
		t.Errorf("the nested repository's file changed: %q", got)
	}
	// None of the refusals left a note.
	if stored, _ := h.store.GetChat(t.Context(), c.ID); stored.Draft != "" {
		t.Errorf("a refused reject wrote the draft: %q", stored.Draft)
	}
}

// TestChatHunkRejectOverTheDraftCap is task 169 decision 8's last clause: a
// note that does not fit never refuses the reject.
func TestChatHunkRejectOverTheDraftCap(t *testing.T) {
	h := newExportHarness(t)
	dir := h.diffWorktree(t, "cap")
	c := h.chat(t, &store.Chat{WorktreePath: dir})
	full := strings.Repeat("x", store.MaxChatDraftBytes-8)
	if _, err := h.store.SetChatDraft(t.Context(), c.ID, full); err != nil {
		t.Fatal(err)
	}
	hk := hunkWith(t, h.chatDiff(t, c.ID, "base"), "long.txt", "+line 25 dirty")
	out := h.reject(t, c.ID, "base", "long.txt", hk.Hash)
	if out.NoteAdded || out.Warning == "" || out.Draft != full {
		t.Errorf("over-cap reject = note_added %v warning %q draft %d bytes", out.NoteAdded, out.Warning, len(out.Draft))
	}
	if got := readFile(t, dir, "long.txt"); got != diffLines(map[int]string{2: "line 2 committed"}) {
		t.Errorf("the hunk was not reverted:\n%s", got)
	}
}

// serveChatDiff is a server over st with no runner, for the case that
// reopens its store.
func serveChatDiff(t *testing.T, st *store.Store, dataDir string) *projectHarness {
	t.Helper()
	reg := agent.NewRegistry()
	s := New(Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Store: st,
		Agents: reg, Git: gitx.New(), Catalog: agent.NewCatalogCache(reg), Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &projectHarness{ts: ts, store: st}
}

func TestChatHunkAcceptMarks(t *testing.T) {
	h := newExportHarness(t)
	dir := h.diffWorktree(t, "marks")
	c := h.chat(t, &store.Chat{WorktreePath: dir})
	other := h.chat(t, &store.Chat{WorktreePath: dir})
	hk := hunkWith(t, h.chatDiff(t, c.ID, "base"), "long.txt", "+line 25 dirty")
	req := map[string]any{"scope": "base", "path": "long.txt", "hunk": hk.Hash}

	// A hash the scope's diff does not hold is stale, and writes no mark.
	for _, bad := range []map[string]any{
		{"scope": "base", "path": "long.txt", "hunk": strings.Repeat("0", 64)},
		{"scope": "base", "path": "nowhere.txt", "hunk": hk.Hash},
	} {
		if status, code, raw := h.hunkCall(t, c.ID, "accept", bad); status != http.StatusConflict || code != CodeChatHunkStale {
			t.Errorf("accept %v = %d %s", bad, status, raw)
		}
	}
	head, status := testrepo.Run(t, dir, "rev-parse", "HEAD"), testrepo.Run(t, dir, "status", "--porcelain")
	for range 2 { // accepting twice is the same mark
		if status, _, raw := h.hunkCall(t, c.ID, "accept", req); status != http.StatusNoContent {
			t.Fatalf("accept = %d %s", status, raw)
		}
	}
	// Only a mark: nothing in git changed.
	if testrepo.Run(t, dir, "rev-parse", "HEAD") != head || testrepo.Run(t, dir, "status", "--porcelain") != status {
		t.Error("accept changed the repository")
	}
	if got := hunkWith(t, h.chatDiff(t, c.ID, "base"), "long.txt", "+line 25 dirty"); !got.Accepted {
		t.Error("the accepted hunk does not read back accepted")
	}
	if got := hunkWith(t, h.chatDiff(t, c.ID, "base"), "long.txt", "+line 2 committed"); got.Accepted {
		t.Error("a hunk nobody accepted reads accepted")
	}
	// A mark is one chat's.
	if got := hunkWith(t, h.chatDiff(t, other.ID, "base"), "long.txt", "+line 25 dirty"); got.Accepted {
		t.Error("another chat on the same directory sees the mark")
	}

	// The mark survives the store being closed and opened again.
	h.ts.Close()
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(h.dataDir, "export.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.projectHarness = serveChatDiff(t, st, h.dataDir)
	if got := hunkWith(t, h.chatDiff(t, c.ID, "base"), "long.txt", "+line 25 dirty"); !got.Accepted {
		t.Error("the mark did not survive a reopen")
	}

	// Unaccept is idempotent, and needs no diff to name the hunk in.
	unreq := map[string]any{"path": "long.txt", "hunk": hk.Hash}
	for range 2 {
		if status, _, raw := h.hunkCall(t, c.ID, "unaccept", unreq); status != http.StatusNoContent {
			t.Fatalf("unaccept = %d %s", status, raw)
		}
	}
	if got := hunkWith(t, h.chatDiff(t, c.ID, "base"), "long.txt", "+line 25 dirty"); got.Accepted {
		t.Error("the hunk is still accepted after unaccept")
	}

	// A hunk whose content changed is another hunk: the mark stops matching.
	if status, _, raw := h.hunkCall(t, c.ID, "accept", req); status != http.StatusNoContent {
		t.Fatalf("accept = %d %s", status, raw)
	}
	testrepo.WriteFile(t, dir, "long.txt", diffLines(map[int]string{2: "line 2 committed", 25: "line 25 dirtier"}))
	changed := hunkWith(t, h.chatDiff(t, c.ID, "base"), "long.txt", "+line 25 dirtier")
	if changed.Accepted || changed.Hash == hk.Hash {
		t.Errorf("the changed hunk kept the mark: %+v", changed)
	}
	// And the old hash is no longer something to accept.
	if status, code, raw := h.hunkCall(t, c.ID, "accept", req); status != http.StatusConflict || code != CodeChatHunkStale {
		t.Errorf("accept of the old hash = %d %s", status, raw)
	}

	// The marks go with the chat.
	if marks, _ := st.ListChatHunkReviews(t.Context(), c.ID); len(marks) != 1 {
		t.Fatalf("marks before the delete = %+v", marks)
	}
	if _, err := st.SetChatState(t.Context(), c.ID, chatstate.Archived); err != nil {
		t.Fatal(err)
	}
	if marks, _ := st.ListChatHunkReviews(t.Context(), c.ID); len(marks) != 1 {
		t.Errorf("archiving dropped the mark: %+v", marks)
	}
	if err := st.DeleteChatCascade(t.Context(), c.ID); err != nil {
		t.Fatalf("DeleteChatCascade: %v", err)
	}
	if marks, _ := st.ListChatHunkReviews(t.Context(), c.ID); len(marks) != 0 {
		t.Errorf("marks outlived their chat: %+v", marks)
	}
}

// TestChatDiffOfALinkedChatIsTheTasksDirectory: a linked chat diffs its
// task's worktree against its task's base (task 169 decision 5), and the
// task diff and the export's diff keep leaving untracked files out
// (decision 1).
func TestChatDiffOfALinkedChatIsTheTasksDirectory(t *testing.T) {
	h := newExportHarness(t)
	dir := h.diffWorktree(t, "linked")
	ctx := t.Context()
	task := &store.Task{
		ProjectID: h.projectID, Title: "linked", WorkflowName: "t", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", State: store.TaskBlocked, WorktreePath: dir,
	}
	branch := func(id int64) (string, error) { return fmt.Sprintf("vincent/%d-linked", id), nil }
	if err := h.store.CreateTask(ctx, task, branch); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	c := &store.Chat{Title: "why", Agent: "claude", PermissionMode: string(agent.FullAuto)}
	if err := h.store.OpenLinkedChat(ctx, task.ID, store.TaskBlocked, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	d := h.chatDiff(t, c.ID, "base")
	if got := diffPaths(d); got != "long.txt,new.txt" {
		t.Fatalf("linked chat diff = %s", got)
	}
	if want := strings.TrimSpace(testrepo.Run(t, dir, "merge-base", "main", "HEAD")); d.From != want {
		t.Errorf("from = %s, want the task's merge-base %s", d.From, want)
	}
	// A reject lands in the task's worktree.
	hk := hunkWith(t, d, "long.txt", "+line 25 dirty")
	if status, _, raw := h.hunkCall(t, c.ID, "reject", map[string]any{"scope": "base", "path": "long.txt", "hunk": hk.Hash}); status != http.StatusOK {
		t.Fatalf("reject = %d %s", status, raw)
	}
	if got := readFile(t, dir, "long.txt"); got != diffLines(map[int]string{2: "line 2 committed"}) {
		t.Errorf("the task's long.txt:\n%s", got)
	}

	// Phase 1's rule stands everywhere else.
	_, taskDiff := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff", task.ID), nil)
	if strings.Contains(string(taskDiff), "new.txt") || !strings.Contains(string(taskDiff), "+line 2 committed") {
		t.Errorf("task diff:\n%s", taskDiff)
	}
	e := h.exportJSON(t, c.ID, "&diff=true")
	if e.Diff == nil || *e.Diff != string(taskDiff) {
		t.Errorf("export diff differs from the task diff: %v (%q)", e.Diff, e.DiffUnavailable)
	}
	free := h.chat(t, &store.Chat{WorktreePath: dir})
	e = h.exportJSON(t, free.ID, "&diff=true")
	if e.Diff == nil || strings.Contains(*e.Diff, "new.txt") {
		t.Errorf("a free chat's export diff carries the untracked file: %v", e.Diff)
	}
}

func TestChatDiffOfTerminalChats(t *testing.T) {
	h := newExportHarness(t)
	dir := h.diffWorktree(t, "ended")
	before := readFile(t, dir, "long.txt")
	for _, state := range []chatstate.State{chatstate.Closed, chatstate.Archived} {
		c := h.chat(t, &store.Chat{State: state, WorktreePath: dir})
		d := h.chatDiff(t, c.ID, "base")
		if got := diffPaths(d); got != "long.txt,new.txt" {
			t.Errorf("%s chat diff = %s", state, got)
		}
		hk := hunkWith(t, d, "long.txt", "+line 25 dirty")
		status, code, raw := h.hunkCall(t, c.ID, "reject", map[string]any{"scope": "base", "path": "long.txt", "hunk": hk.Hash})
		if status != http.StatusConflict || code != CodeInvalidState {
			t.Errorf("reject on a %s chat = %d %s", state, status, raw)
		}
		if got := readFile(t, dir, "long.txt"); got != before {
			t.Errorf("reject on a %s chat changed the worktree", state)
		}
	}
	// No directory: unavailable, with the export's own reasons.
	gone := filepath.Join(t.TempDir(), "gone")
	for name, tc := range map[string]struct {
		chat *store.Chat
		why  string
	}{
		"handed off":  {&store.Chat{State: chatstate.HandedOff, WorktreePath: dir}, "handed off"},
		"no worktree": {&store.Chat{State: chatstate.Archived}, "the chat has no worktree"},
		"missing dir": {&store.Chat{State: chatstate.Closed, WorktreePath: gone}, "no longer exists"},
		"no base":     {&store.Chat{WorktreePath: dir, BaseBranch: "nowhere"}, "merge-base"},
	} {
		c := h.chat(t, tc.chat)
		for _, path := range []string{"/diff", "/diff/stat"} {
			resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d%s", c.ID, path), nil)
			if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), CodeChatDiffUnavailable) ||
				!strings.Contains(string(body), tc.why) {
				t.Errorf("%s %s = %d %s", name, path, resp.StatusCode, body)
			}
		}
	}
	if resp, _ := h.doJSON(t, http.MethodGet, "/v1/chats/9999/diff", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown chat = %d", resp.StatusCode)
	}
}

// TestChatHunkRefusedByStateWithoutARunner: a chat whose row says it holds a
// process is live whether or not a goroutine is behind it — a daemon that
// has not recovered it yet.
func TestChatHunkRefusedByStateWithoutARunner(t *testing.T) {
	h := newExportHarness(t)
	dir := h.diffWorktree(t, "rowlive")
	before := readFile(t, dir, "long.txt")
	for _, state := range []chatstate.State{chatstate.Running, chatstate.AwaitingInput} {
		c := h.chat(t, &store.Chat{State: state, WorktreePath: dir})
		hk := hunkWith(t, h.chatDiff(t, c.ID, "base"), "long.txt", "+line 25 dirty") // reading is allowed
		for _, verb := range []string{"reject", "accept"} {
			status, code, raw := h.hunkCall(t, c.ID, verb, map[string]any{"scope": "base", "path": "long.txt", "hunk": hk.Hash})
			if status != http.StatusConflict || code != CodeChatTurnLive {
				t.Errorf("%s on a %s chat = %d %s", verb, state, status, raw)
			}
		}
		if marks, _ := h.store.ListChatHunkReviews(t.Context(), c.ID); len(marks) != 0 {
			t.Errorf("a refused accept wrote a mark: %+v", marks)
		}
	}
	if got := readFile(t, dir, "long.txt"); got != before {
		t.Error("a refused reject changed the worktree")
	}
}

// TestChatHunkRefusedDuringALiveTurn is task 169 decision 7 through the real
// runner: while the fake agent holds a turn open, the diff still reads, and
// reject and accept are 409 chat_turn_live with the worktree untouched. The
// chat is an issue chat, so this is also decision 5's third kind: it diffs
// the issue's main worktree against the issue chat's base.
func TestChatHunkRefusedDuringALiveTurn(t *testing.T) {
	h := newChatHarness(t)
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	iss, err := h.store.CreateIssue(t.Context(), store.NewIssue{
		ProjectID: h.projectID, Title: "Diff the issue", Body: "b",
	}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	id, _, dir := h.openIssueChat(t, iss.ID)
	testrepo.WriteFile(t, dir, "issue.txt", "in the issue worktree\n")

	read := func() chatDiffBody {
		t.Helper()
		resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/diff", id), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("diff = %d %s", resp.StatusCode, body)
		}
		var d chatDiffBody
		if err := json.Unmarshal(body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	d := read()
	if got := diffPaths(d); got != "issue.txt" {
		t.Fatalf("issue chat diff = %s", got)
	}
	if want := strings.TrimSpace(testrepo.Run(t, h.repo, "rev-parse", "main")); d.From != want {
		t.Errorf("from = %s, want main's %s", d.From, want)
	}
	hk := hunkWith(t, d, "issue.txt", "+in the issue worktree")
	req := map[string]any{"scope": "base", "path": "issue.txt", "hunk": hk.Hash}

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/send", id), map[string]any{"message": "hold"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send = %d %s", resp.StatusCode, body)
	}
	waitUntil(t, func() bool { return h.runner.Running(id) })
	if got := diffPaths(read()); got != "issue.txt" {
		t.Errorf("diff during the live turn = %s", got)
	}
	for _, verb := range []string{"reject", "accept"} {
		if status, code, raw := h.hunkCall(t, id, verb, req); status != http.StatusConflict || code != CodeChatTurnLive {
			t.Errorf("%s during a live turn = %d %s", verb, status, raw)
		}
	}
	if got := readFile(t, dir, "issue.txt"); got != "in the issue worktree\n" {
		t.Errorf("the worktree changed under a live turn: %q", got)
	}
	if marks, _ := h.store.ListChatHunkReviews(t.Context(), id); len(marks) != 0 {
		t.Errorf("a refused accept wrote a mark: %+v", marks)
	}

	// Once the turn is gone the same reject goes through.
	h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/cancel", id), nil)
	waitUntil(t, func() bool {
		c, err := h.store.GetChat(t.Context(), id)
		return err == nil && c.State == chatstate.Idle
	})
	waitUntil(t, func() bool {
		status, _, _ := h.hunkCall(t, id, "reject", req)
		return status == http.StatusOK
	})
	if _, err := os.Stat(filepath.Join(dir, "issue.txt")); !os.IsNotExist(err) {
		t.Errorf("issue.txt survived the reject: %v", err)
	}
}
