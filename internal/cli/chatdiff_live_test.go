package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/testrepo"
)

// `vincent chat diff` and its `reject` and `accept` (task 169) against the
// real handlers and real git.

// chatDiffDir is a repository whose working tree is two hunks and an
// untracked file away from main.
func chatDiffDir(t *testing.T) string {
	t.Helper()
	dir := diffRepo(t)
	lines := func(edit map[int]string) string {
		var b strings.Builder
		for i := 1; i <= 20; i++ {
			if v, ok := edit[i]; ok {
				b.WriteString(v + "\n")
				continue
			}
			b.WriteString("line " + strconv.Itoa(i) + "\n")
		}
		return b.String()
	}
	testrepo.WriteFile(t, dir, "long.txt", lines(nil))
	testrepo.Run(t, dir, "add", "long.txt")
	testrepo.Run(t, dir, "commit", "-q", "-m", "long")
	testrepo.WriteFile(t, dir, "long.txt", lines(map[int]string{2: "second", 18: "eighteenth"}))
	testrepo.WriteFile(t, dir, "new.txt", "untracked\n")
	return dir
}

func TestChatDiffCommand(t *testing.T) {
	h := newLiveHarness(t)
	dir := chatDiffDir(t)
	chat := h.addFilesChat(t, "claude", dir, chatstate.Idle)
	id := strconv.FormatInt(chat.ID, 10)

	out, errOut, code := runCLI(t, "chat", "diff", id, "--json")
	if code != 0 {
		t.Fatalf("diff --json exit = %d (%s)", code, errOut)
	}
	var d apiclient.ChatDiff
	if err := json.Unmarshal([]byte(out), &d); err != nil || len(d.Files) != 2 || len(d.Files[0].Hunks) != 2 {
		t.Fatalf("diff --json = %s (%v)", out, err)
	}
	second, eighteenth := d.Files[0].Hunks[0], d.Files[0].Hunks[1]

	// The listing: a row per file, a row per hunk with its hash prefix.
	out, errOut, code = runCLI(t, "chat", "diff", id)
	if code != 0 {
		t.Fatalf("diff exit = %d (%s)", code, errOut)
	}
	for _, want := range []string{
		"modified\tlong.txt\t+2\t-2\n",
		"    " + second.Hash[:12] + "\t" + second.Header + "\n",
		"added\tnew.txt\t+1\t-0\n",
		"2 files\t+3\t-2\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, out)
		}
	}

	out, errOut, code = runCLI(t, "chat", "diff", id, "--stat")
	if code != 0 || out != "2 files\t+3\t-2\n" {
		t.Errorf("--stat = %q exit %d (%s)", out, code, errOut)
	}
	out, _, code = runCLI(t, "chat", "diff", id, "--stat", "--json")
	var st apiclient.ChatDiffStat
	if err := json.Unmarshal([]byte(out), &st); err != nil || code != 0 || st.Files != 2 || st.Added != 3 {
		t.Errorf("--stat --json = %q exit %d (%v)", out, code, err)
	}

	// Refusals: a bad scope before any request, a scope with nothing to
	// diff from, an unknown chat.
	if _, _, code := runCLI(t, "chat", "diff", id, "--scope", "all"); code == 0 {
		t.Error("--scope all succeeded")
	}
	if _, errOut, code := runCLI(t, "chat", "diff", id, "--scope", "turn"); code != 1 || !strings.Contains(errOut, "no turn") {
		t.Errorf("--scope turn exit = %d (%s), want 1 with the reason", code, errOut)
	}
	if _, _, code := runCLI(t, "chat", "diff", "9999"); code != 1 {
		t.Errorf("unknown chat exit = %d, want 1", code)
	}

	// Accept by a unique prefix; the listing marks it; --undo takes it back.
	out, errOut, code = runCLI(t, "chat", "diff", "accept", id, "long.txt", second.Hash[:10])
	if code != 0 || out != "accepted long.txt "+second.Hash[:12]+"\n" {
		t.Fatalf("accept = %q exit %d (%s)", out, code, errOut)
	}
	if out, _, _ := runCLI(t, "chat", "diff", id); !strings.Contains(out, "  * "+second.Hash[:12]+"\t") {
		t.Errorf("listing does not mark the accepted hunk:\n%s", out)
	}
	if out, errOut, code := runCLI(t, "chat", "diff", "accept", id, "long.txt", second.Hash, "--undo"); code != 0 ||
		!strings.HasPrefix(out, "unaccepted long.txt ") {
		t.Fatalf("accept --undo = %q exit %d (%s)", out, code, errOut)
	}
	if out, _, _ := runCLI(t, "chat", "diff", id); strings.Contains(out, "  * ") {
		t.Errorf("listing still marks a hunk after --undo:\n%s", out)
	}

	// --undo with the full hash reads no diff (review F9): the mark of a hunk
	// whose content has since changed can still be taken back. A prefix has
	// nothing left to resolve against and is refused.
	fresh := d.Files[1].Hunks[0]
	if _, errOut, code := runCLI(t, "chat", "diff", "accept", id, "new.txt", fresh.Hash); code != 0 {
		t.Fatalf("accept new.txt exit %d (%s)", code, errOut)
	}
	testrepo.WriteFile(t, dir, "new.txt", "untracked, and edited since\n")
	if _, errOut, code := runCLI(t, "chat", "diff", "accept", id, "new.txt", fresh.Hash[:10], "--undo"); code != 1 ||
		!strings.Contains(errOut, "no hunk") {
		t.Errorf("--undo by prefix of a changed hunk: exit %d (%s)", code, errOut)
	}
	if out, errOut, code := runCLI(t, "chat", "diff", "accept", id, "new.txt", strings.ToUpper(fresh.Hash), "--undo", "--scope", "turn"); code != 0 ||
		out != "unaccepted new.txt "+fresh.Hash[:12]+"\n" {
		t.Fatalf("--undo by full hash of a changed hunk = %q exit %d (%s)", out, code, errOut)
	}
	// With the content back, the hunk is the same hunk — and carries no mark.
	testrepo.WriteFile(t, dir, "new.txt", "untracked\n")
	if out, _, _ := runCLI(t, "chat", "diff", id); strings.Contains(out, "  * ") {
		t.Errorf("the mark outlived --undo:\n%s", out)
	}

	// A prefix that is no hunk's, and an ambiguous one, are refused here.
	if _, errOut, code := runCLI(t, "chat", "diff", "reject", id, "long.txt", "zz"); code != 1 || !strings.Contains(errOut, "no hunk") {
		t.Errorf("unknown prefix exit = %d (%s)", code, errOut)
	}
	if _, errOut, code := runCLI(t, "chat", "diff", "reject", id, "elsewhere.txt", second.Hash); code != 1 ||
		!strings.Contains(errOut, "not in the chat's current diff") {
		t.Errorf("unknown path exit = %d (%s)", code, errOut)
	}

	// Reject: exactly that hunk leaves the working tree, and the chat's
	// draft holds the note.
	out, errOut, code = runCLI(t, "chat", "diff", "reject", id, "long.txt", eighteenth.Hash[:12], "--json")
	if code != 0 {
		t.Fatalf("reject exit = %d (%s)", code, errOut)
	}
	var res apiclient.ChatHunkReject
	if err := json.Unmarshal([]byte(out), &res); err != nil || !res.NoteAdded ||
		!strings.Contains(res.Draft, "- long.txt "+eighteenth.Header) {
		t.Errorf("reject --json = %s (%v)", out, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "long.txt"))
	if err != nil || strings.Contains(string(got), "eighteenth") || !strings.Contains(string(got), "second") {
		t.Errorf("long.txt after the reject (%v):\n%s", err, got)
	}
	stored, err := h.st.GetChat(t.Context(), chat.ID)
	if err != nil || stored.Draft != res.Draft {
		t.Errorf("stored draft = %q, %v", stored.Draft, err)
	}
	if out, errOut, code := runCLI(t, "chat", "diff", "reject", id, "new.txt", d.Files[1].Hunks[0].Hash); code != 0 ||
		!strings.HasPrefix(out, "rejected new.txt ") {
		t.Errorf("plain reject = %q exit %d (%s)", out, code, errOut)
	}

	// A chat with a live turn refuses, with the daemon's reason.
	busy := h.addFilesChat(t, "claude", dir, chatstate.Running)
	if _, errOut, code := runCLI(t, "chat", "diff", "reject", strconv.FormatInt(busy.ID, 10), "long.txt", second.Hash); code != 1 ||
		!strings.Contains(errOut, "live turn") {
		t.Errorf("reject on a live chat exit = %d (%s)", code, errOut)
	}
}

func TestChatHunkPrefixResolution(t *testing.T) {
	d := &apiclient.ChatDiff{Files: []apiclient.ChatDiffFile{{
		Path:  "a.go",
		Hunks: []apiclient.ChatDiffHunk{{Hash: "abc123"}, {Hash: "abd456"}, {Hash: "ab"}},
	}}}
	for prefix, want := range map[string]string{"abc": "abc123", "ABD4": "abd456", "abc123": "abc123", "ab": "ab"} {
		if got, err := resolveChatHunk(d, "a.go", prefix); err != nil || got != want {
			t.Errorf("resolve %q = %q, %v; want %q", prefix, got, err, want)
		}
	}
	for _, bad := range []string{"a", "x"} {
		if got, err := resolveChatHunk(d, "a.go", bad); err == nil {
			t.Errorf("resolve %q = %q, want an error", bad, got)
		}
	}
	if _, err := resolveChatHunk(d, "b.go", "abc"); err == nil {
		t.Error("an unknown path resolved")
	}
}
