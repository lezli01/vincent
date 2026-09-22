package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// `vincent chat files` against the real GET /v1/chats/{id}/files handler, a
// real worktree manager and a real git repository (task 126.7). The command
// renders only what the route answered and re-derives nothing, so only the
// route can say what that is — a hand-written stub here would be the one place
// the CLI and the daemon could drift apart unnoticed.

// newChatFilesLive is the transcript command's harness with a worktree manager
// on the API, serving claude and one adapter that cannot mention files.
//
// claude is the real adapter rather than a stub because the `@"…"`-on-a-space
// quoting rule is written there, and the whole point of the route is that no
// client rebuilds it. Nothing launches it: the two mention methods are static.
func newChatFilesLive(t *testing.T) *liveHarness {
	t.Helper()
	return newLiveHarness(t, withFileListing(
		agent.NewRegistry(claude.New(func() string { return "claude" }), agenttest.StubNoMentions{})))
}

// filesRepo is testrepo.Init with the developer's own global core.excludesFile
// taken out of the answer, the way internal/worktree's file tests and
// apiclient's do it: a personal global ignore rule would otherwise remove a
// row on one machine and not on another.
func filesRepo(t *testing.T) string {
	t.Helper()
	dir := testrepo.Init(t, "main")
	return withoutGlobalExcludes(t, dir)
}

func withoutGlobalExcludes(t *testing.T, dir string) string {
	t.Helper()
	excludes := filepath.Join(t.TempDir(), "empty-excludes")
	if err := os.WriteFile(excludes, nil, 0o644); err != nil {
		t.Fatalf("write empty excludes file: %v", err)
	}
	testrepo.Run(t, dir, "config", "core.excludesFile", excludes)
	return dir
}

// addFilesChat writes a free chat on agentName whose worktree is dir. Rows go
// through the store because POST /v1/chats would cut a worktree of its own,
// and the directory under test is the point.
func (h *liveHarness) addFilesChat(
	t *testing.T, agentName, dir string, state chatstate.State,
) *store.Chat {
	t.Helper()
	c := &store.Chat{
		ProjectID: h.projectID, Title: "files", State: state, Agent: agentName,
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: dir,
	}
	if err := h.st.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return c
}

// chatFileLines splits stdout into its rows, dropping the trailing newline's
// empty tail. A listing of nothing is no rows, not one empty one.
func chatFileLines(out string) []string {
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// chatFilesJSON runs the command with --json and decodes the response object.
func chatFilesJSON(t *testing.T, args ...string) *apiclient.ChatFiles {
	t.Helper()
	out, errOut, code := runCLI(t, append([]string{"chat", "files"}, append(args, "--json")...)...)
	if code != 0 {
		t.Fatalf("chat files --json: exit %d (stderr %q)", code, errOut)
	}
	var got apiclient.ChatFiles
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return &got
}

// The default output is one workspace-relative path per line, in the route's
// own order, and stdout carries nothing else (task 126 decision 21).
//
// The order assertion has teeth because of how the fixture is built:
// `git ls-files --cached --others` prints the untracked rows first and the
// indexed ones after, so `n.txt` precedes `README.md` and `m.txt` — an order
// no sort would produce. Comparing against the object --json emits is what
// makes the claim "the route's order" rather than "this order".
func TestChatFilesCommandPrintsThePaths(t *testing.T) {
	h := newChatFilesLive(t)
	dir := filesRepo(t)
	testrepo.WriteFile(t, dir, "m.txt", "x\n")
	testrepo.Run(t, dir, "add", "m.txt")
	testrepo.Run(t, dir, "commit", "-q", "-m", "tracked")
	// Untracked, and written after the chat's directory was committed: a file
	// created a minute ago is exactly the file someone wants to point an
	// agent at (§5.5).
	testrepo.WriteFile(t, dir, "n.txt", "x\n")
	chat := h.addFilesChat(t, "claude", dir, chatstate.Idle)
	id := strconv.FormatInt(chat.ID, 10)

	out, errOut, code := runCLI(t, "chat", "files", id)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	got := chatFileLines(out)
	want := []string{}
	for _, f := range chatFilesJSON(t, id).Files {
		want = append(want, f.Path)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("stdout = %v, want the route's rows in the route's order %v", got, want)
	}
	if len(want) != 3 {
		t.Fatalf("the fixture listed %v, want README.md, m.txt and n.txt", want)
	}
	// If this ever fails, git stopped printing its untracked rows first and
	// the fixture no longer witnesses "unsorted" — the claim needs a new one,
	// not a sorted expectation.
	if slices.IsSorted(want) {
		t.Errorf("git's own order %v is the sorted one, so this fixture proves "+
			"nothing about sorting", want)
	}
	// stdout is the listing and nothing else, which is what makes
	// `vincent chat files 12 | wc -l` a count of files.
	if errOut != "" {
		t.Errorf("stderr carries something for an untruncated listing: %q", errOut)
	}
}

// --mention prints the daemon's own mention text, including the one form no
// client may rebuild: a path with a space, double-quoted after the sigil
// (§5.5's path-form table, task 126 decision 1).
func TestChatFilesCommandMentionPrintsTheDaemonsText(t *testing.T) {
	h := newChatFilesLive(t)
	dir := filesRepo(t)
	testrepo.WriteFile(t, dir, filepath.Join("dir with space", "b.txt"), "x\n")
	chat := h.addFilesChat(t, "claude", dir, chatstate.Idle)
	id := strconv.FormatInt(chat.ID, 10)

	out, errOut, code := runCLI(t, "chat", "files", id, "--mention")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	got := chatFileLines(out)
	want := []string{}
	for _, f := range chatFilesJSON(t, id).Files {
		want = append(want, f.Mention)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("stdout = %v, want the route's mention strings %v", got, want)
	}
	if !slices.Contains(got, `@"dir with space/b.txt"`) {
		t.Errorf("the spaced path is not quoted as claude quotes it: %v", got)
	}
	// The path is what the CLI must *not* have printed here: an unquoted
	// `@dir with space/b.txt` would be the command having built a mention of
	// its own, which claude does not expand.
	if slices.Contains(got, "@dir with space/b.txt") {
		t.Errorf("the CLI re-quoted a path itself instead of printing the daemon's text: %v", got)
	}
}

// --mention and --json are two spellings of stdout and cannot both be it.
func TestChatFilesCommandMentionAndJSONAreExclusive(t *testing.T) {
	h := newChatFilesLive(t)
	chat := h.addFilesChat(t, "claude", filesRepo(t), chatstate.Idle)

	out, errOut, code := runCLI(t, "chat", "files",
		strconv.FormatInt(chat.ID, 10), "--mention", "--json")
	if code == 0 {
		t.Fatalf("exit = 0 for --mention --json (stdout %q)", out)
	}
	if !strings.Contains(errOut, "mention") || !strings.Contains(errOut, "json") {
		t.Errorf("stderr does not name the two flags: %q", errOut)
	}
}

// --json is the response object unchanged, and `files` is an array even when
// the listing is empty: a script's `| jq '.files[]'` must not fail on a type.
func TestChatFilesCommandJSON(t *testing.T) {
	h := newChatFilesLive(t)
	dir := filesRepo(t)
	chat := h.addFilesChat(t, "claude", dir, chatstate.Idle)

	got := chatFilesJSON(t, strconv.FormatInt(chat.ID, 10))
	if got.ChatID != chat.ID || got.Agent != "claude" || got.WorkDir != dir {
		t.Errorf("identity = (%d, %q, %q), want (%d, %q, %q)",
			got.ChatID, got.Agent, got.WorkDir, chat.ID, "claude", dir)
	}
	if got.MentionSigil != "@" || got.MentionPosition != string(agent.MentionAnywhere) ||
		!got.MentionExpands {
		t.Errorf("mention facts = (%q, %q, %v), want (@, anywhere, true)",
			got.MentionSigil, got.MentionPosition, got.MentionExpands)
	}
	if got.Truncated {
		t.Error("truncated is true for a listing of one file")
	}
	if len(got.Files) != 1 || got.Files[0].Path != "README.md" ||
		got.Files[0].Mention != "@README.md" {
		t.Errorf("files = %+v, want one README.md with its mention", got.Files)
	}

	// An empty listing is `[]` and never `null`, which is asserted on the raw
	// bytes: the typed decode above would turn either into an empty slice.
	empty := h.addFilesChat(t, "claude", withoutGlobalExcludes(t, testrepo.InitEmpty(t, "main")),
		chatstate.Idle)
	out, errOut, code := runCLI(t, "chat", "files", strconv.FormatInt(empty.ID, 10), "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	var raw struct {
		Files []apiclient.ChatFile `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if raw.Files == nil {
		t.Errorf("an empty listing sent null rather than an array: %q", out)
	}
	// And without --json it prints nothing at all, still at exit 0.
	out, errOut, code = runCLI(t, "chat", "files", strconv.FormatInt(empty.ID, 10))
	if code != 0 || out != "" || errOut != "" {
		t.Errorf("an empty listing printed (%q, %q) at exit %d, want nothing at 0", out, errOut, code)
	}
}

// A cut listing keeps stdout clean and says so on stderr, still at exit 0: a
// truncated answer is an answer (task 126 decision 36).
func TestChatFilesCommandTruncatedWarnsAtExitZero(t *testing.T) {
	h := newChatFilesLive(t)
	dir := filesRepo(t)
	testrepo.WriteFile(t, dir, "a.txt", "x\n")
	chat := h.addFilesChat(t, "claude", dir, chatstate.Idle)
	id := strconv.FormatInt(chat.ID, 10)

	out, errOut, code := runCLI(t, "chat", "files", id, "--limit", "1")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if got := chatFileLines(out); len(got) != 1 {
		t.Fatalf("stdout = %v, want the one row --limit 1 asked for", got)
	}
	if !strings.Contains(errOut, "the listing was cut at 1 file(s)") {
		t.Errorf("stderr does not say the listing was cut: %q", errOut)
	}
	// The flag reached the wire rather than trimming a full answer here.
	if got := chatFilesJSON(t, id, "--limit", "1"); len(got.Files) != 1 || !got.Truncated {
		t.Errorf("--limit 1 = %d row(s), truncated %v; want 1 and true", len(got.Files), got.Truncated)
	}
	// Without the flag there is no warning and both rows arrive.
	out, errOut, code = runCLI(t, "chat", "files", id)
	if code != 0 || errOut != "" {
		t.Fatalf("the unlimited listing exited %d with stderr %q", code, errOut)
	}
	if got := chatFileLines(out); len(got) != 2 {
		t.Errorf("stdout = %v, want both files", got)
	}
}

// An adapter that is not a FileMentioner is an empty sigil, not a refusal
// (task 126 decision 38): --mention prints no rows and one note on stderr,
// and the command still exits 0. This is the branch the gate cannot reach,
// since all three shipped adapters mention files.
func TestChatFilesCommandWithoutAMentionCapableAdapter(t *testing.T) {
	h := newChatFilesLive(t)
	chat := h.addFilesChat(t, agenttest.NoMentionsName, filesRepo(t), chatstate.Idle)
	id := strconv.FormatInt(chat.ID, 10)

	out, errOut, code := runCLI(t, "chat", "files", id, "--mention")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if out != "" {
		t.Errorf("stdout carries mention text for an adapter that cannot mention: %q", out)
	}
	if !strings.Contains(errOut, "no mention text: "+agenttest.NoMentionsName+
		" cannot mention files") {
		t.Errorf("stderr does not say why there is no mention text: %q", errOut)
	}
	// The paths are true regardless of who reads them, so they are still
	// served and still printed without the flag.
	out, errOut, code = runCLI(t, "chat", "files", id)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if got := chatFileLines(out); !slices.Equal(got, []string{"README.md"}) {
		t.Errorf("stdout = %v, want the paths an unmentionable adapter still has", got)
	}
}

// The daemon's refusals are exit 1 with its own words: exit 0 is for a
// question it answered, and a script must not confuse the two.
func TestChatFilesCommandRefusalsExitOne(t *testing.T) {
	h := newChatFilesLive(t)

	t.Run("unknown chat", func(t *testing.T) {
		out, errOut, code := runCLI(t, "chat", "files", "9999")
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout %q, stderr %q)", code, out, errOut)
		}
		if !strings.HasPrefix(errOut, "Error: ") {
			t.Errorf("stderr does not carry the daemon's message: %q", errOut)
		}
	})

	t.Run("terminal chat", func(t *testing.T) {
		chat := h.addFilesChat(t, "claude", filesRepo(t), chatstate.Archived)
		out, errOut, code := runCLI(t, "chat", "files", strconv.FormatInt(chat.ID, 10))
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout %q, stderr %q)", code, out, errOut)
		}
		if !strings.Contains(errOut, "no next turn to list files for") {
			t.Errorf("stderr does not say why: %q", errOut)
		}
	})
}

// No daemon is exit 2 through the shared withClient path: a script must be
// able to tell "start the daemon" from "fix your request" (PR U decision).
func TestChatFilesCommandWithNoDaemonExitsTwo(t *testing.T) {
	t.Setenv(config.EnvDataDir, t.TempDir())
	t.Setenv(config.EnvConfigDir, t.TempDir())
	out, errOut, code := runCLI(t, "chat", "files", "1")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %q, stderr %q)", code, out, errOut)
	}
	if !strings.Contains(errOut, "no running daemon found") {
		t.Errorf("stderr does not name the missing daemon: %q", errOut)
	}
}
