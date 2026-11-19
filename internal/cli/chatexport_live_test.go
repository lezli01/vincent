package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatexport"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// `vincent chat export` (task 156) against the real GET /v1/chats/{id}/export
// handler. The command carries the daemon's bytes and renders nothing itself,
// so the assertions compare what it wrote with what the route serves.

func exportArgs(chat *store.Chat, extra ...string) []string {
	return append([]string{"chat", "export", strconv.FormatInt(chat.ID, 10)}, extra...)
}

func TestChatExportStdoutAndFile(t *testing.T) {
	h := newLiveHarness(t)
	chat := h.addChat(t)
	h.addTurn(t, chat, chatstate.TurnDone, "", lineOutput, lineResult)

	out, errOut, code := runCLI(t, exportArgs(chat)...)
	if code != 0 {
		t.Fatalf("exit = %d (%s)", code, errOut)
	}
	if !strings.HasPrefix(out, "# Chat ") || !strings.Contains(out, "## Turn 1 · done") {
		t.Errorf("stdout is not the Markdown export:\n%s", out)
	}

	path := filepath.Join(t.TempDir(), "talk.html")
	out, errOut, code = runCLI(t, exportArgs(chat, "--format", "html", "-o", path)...)
	if code != 0 || out != "" {
		t.Fatalf("-o exit = %d, stdout %q (%s)", code, out, errOut)
	}
	page, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(page), "<!DOCTYPE html>") {
		t.Fatalf("written file = %q, %v", page, err)
	}
	// Nothing but the file is left beside it: the write went through a
	// temporary file that was renamed into place.
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("directory holds %d entries, want 1", len(entries))
	}

	// A file that cannot be written is exit 1, with stdout untouched.
	bad := filepath.Join(t.TempDir(), "missing", "x.md")
	if _, errOut, code := runCLI(t, exportArgs(chat, "-o", bad)...); code != 1 || errOut == "" {
		t.Errorf("unwritable -o exit = %d (%s), want 1", code, errOut)
	}
}

func TestChatExportJSONSynonym(t *testing.T) {
	h := newLiveHarness(t)
	chat := h.addChat(t)
	h.addTurn(t, chat, chatstate.TurnDone, "", lineResult)

	for _, args := range [][]string{{"--json"}, {"--format", "json"}, {"--json", "--format", "json"}} {
		out, errOut, code := runCLI(t, exportArgs(chat, args...)...)
		if code != 0 {
			t.Fatalf("%v exit = %d (%s)", args, code, errOut)
		}
		var e chatexport.Export
		if err := json.Unmarshal([]byte(out), &e); err != nil || e.Version != chatexport.Version || e.Chat.ID != chat.ID {
			t.Errorf("%v: not the JSON export (%v):\n%s", args, err, out)
		}
	}
	if _, errOut, code := runCLI(t, exportArgs(chat, "--json", "--format", "html")...); code == 0 ||
		!strings.Contains(errOut, "--json is --format json") {
		t.Errorf("--json --format html exit = %d (%s), want a refusal", code, errOut)
	}
	if _, _, code := runCLI(t, exportArgs(chat, "--format", "pdf")...); code == 0 {
		t.Error("--format pdf succeeded")
	}
	if _, errOut, code := runCLI(t, "chat", "export", "999"); code != 1 {
		t.Errorf("unknown chat exit = %d (%s), want 1", code, errOut)
	}
}

func TestChatExportWithDiff(t *testing.T) {
	h := newLiveHarness(t)
	dir := diffRepo(t)
	testrepo.WriteFile(t, dir, "README.md", "changed\n")
	chat := h.addFilesChat(t, "claude", dir, chatstate.Idle)

	out, errOut, code := runCLI(t, exportArgs(chat, "--with-diff")...)
	if code != 0 {
		t.Fatalf("exit = %d (%s)", code, errOut)
	}
	if !strings.Contains(out, "## Diff") || !strings.Contains(out, "+changed") {
		t.Errorf("export lacks the diff:\n%s", out)
	}
	out, _, _ = runCLI(t, exportArgs(chat)...)
	if strings.Contains(out, "## Diff") {
		t.Errorf("export without --with-diff carries a diff:\n%s", out)
	}
}
