package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/tui"
)

// `vincent chat send` grew an in-progress indicator (task 089, issue #330),
// and the point of these tests is the half of that feature that is a promise
// about what it does *not* print: a piped or redirected send, and every send
// under --json, must emit exactly the bytes it emitted before the indicator
// existed. Cobra's writers here are *bytes.Buffer, which isTTY refuses on the
// type assertion alone, so that promise is structural rather than mocked.

// chatSendStub serves the two endpoints `chat send` calls. The turn is
// running on the first GET and settled on the second, so the command spends a
// real poll interval in the window the indicator would draw in.
func chatSendStub(t *testing.T, state, result string) http.HandlerFunc {
	t.Helper()
	h, _ := recordingChatStub(t, state, result)
	return h
}

// chatStubLog is what a recordingChatStub was asked (task 124.5): every
// request that reached it, the message of each send decoded from the body the
// CLI POSTed, and how many chats were created. It is the body, not the
// command's argument, that --message-file's byte-for-byte promise is about.
type chatStubLog struct {
	mu       sync.Mutex
	requests int
	created  int
	sent     []string
}

func (l *chatStubLog) snapshot() (requests, created int, sent []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.requests, l.created, append([]string(nil), l.sent...)
}

// recordingChatStub is chatSendStub that also answers `POST /v1/chats` for
// `chat start`, as chat 3, and keeps a log of what it was asked.
func recordingChatStub(t *testing.T, state, result string) (http.HandlerFunc, *chatStubLog) {
	t.Helper()
	var polls atomic.Int32
	log := &chatStubLog{}
	return func(w http.ResponseWriter, r *http.Request) {
		log.mu.Lock()
		log.requests++
		log.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.URL.Path == "/v1/chats" && r.Method == http.MethodPost:
			log.mu.Lock()
			log.created++
			log.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":3,"title":"talk","agent":"claude","state":"idle",
				"branch":"vincent/3-talk"}`))
		case r.URL.Path == "/v1/chats/3/send" && r.Method == http.MethodPost:
			var body struct {
				Message *string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Message == nil {
				t.Errorf("send body does not carry a message: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			log.mu.Lock()
			log.sent = append(log.sent, *body.Message)
			log.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":11,"chat_id":3,"seq":1,"state":"running",
				"started_at":"2026-09-05T10:00:00Z"}`))
		case r.URL.Path == "/v1/chats/3" && r.Method == http.MethodGet:
			turn := `{"id":11,"chat_id":3,"seq":1,"state":"running",
				"started_at":"2026-09-05T10:00:00Z"}`
			if polls.Add(1) > 1 {
				turn = `{"id":11,"chat_id":3,"seq":1,"state":"` + state + `",
					"fail_reason":"agent_error","error_message":"boom",
					"result_text":` + strconv.Quote(result) + `,
					"started_at":"2026-09-05T10:00:00Z"}`
			}
			_, _ = w.Write([]byte(`{"chat":{"id":3,"state":"running"},"turns":[` + turn + `]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}, log
}

// runChatSend runs the command against a stub daemon with stdout and stderr
// kept apart, which is the whole question here.
func runChatSend(t *testing.T, h http.HandlerFunc, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runChatSendStdin(t, h, "", args...)
}

// runChatSendStdin is runChatSend with stdin, for `--message-file -`.
func runChatSendStdin(t *testing.T, h http.HandlerFunc, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv(config.EnvDataDir, dataDir)
	t.Setenv(config.EnvConfigDir, t.TempDir())

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse stub URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("stub port: %v", err)
	}
	if _, err := daemon.EnsureToken(dataDir); err != nil {
		t.Fatalf("token: %v", err)
	}
	if err := daemon.WriteRuntimeInfo(dataDir, daemon.RuntimeInfo{
		Port: port, PID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("daemon.json: %v", err)
	}

	var out, errb bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	code = asExitCode(root.ExecuteContext(context.Background()))
	return out.String(), errb.String(), code
}

// TestChatSendWritesNothingExtraWhenRedirected is the acceptance criterion
// that a redirected send is byte-identical: the answer alone on stdout, and
// stderr untouched — no frame, and no erase sequence either.
func TestChatSendWritesNothingExtraWhenRedirected(t *testing.T) {
	stdout, stderr, code := runChatSend(t, chatSendStub(t, "done", "the answer"),
		"chat", "send", "3", "hello")
	if code != 0 {
		t.Fatalf("exit code %d, want 0 (stderr %q)", code, stderr)
	}
	if stdout != "the answer\n" {
		t.Fatalf("stdout = %q, want the answer alone", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want nothing at all when it is not a terminal", stderr)
	}
}

// TestChatSendJSONIsUnchanged holds the --json suppression: the indicator is
// off whether or not stderr is a terminal, so a script's parse is safe.
func TestChatSendJSONIsUnchanged(t *testing.T) {
	stdout, stderr, code := runChatSend(t, chatSendStub(t, "done", "the answer"),
		"chat", "send", "3", "hello", "--json")
	if code != 0 {
		t.Fatalf("exit code %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.HasPrefix(stdout, "{") || !strings.Contains(stdout, `"result_text": "the answer"`) {
		t.Fatalf("stdout is not the turn's JSON: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want nothing under --json", stderr)
	}
}

// TestChatSendFailureIsUnchanged covers the third exit path: a failed turn
// still reports on stderr and exits 1, with nothing before the message.
func TestChatSendFailureIsUnchanged(t *testing.T) {
	stdout, stderr, code := runChatSend(t, chatSendStub(t, "failed", ""),
		"chat", "send", "3", "hello")
	if code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing on a failed turn", stdout)
	}
	// A prefix, not an equality: cobra appends its own "Error: exit code 1"
	// for the returned exitError, which predates this change. What is being
	// held is that nothing — no frame, no erase — precedes the failure line.
	if !strings.HasPrefix(stderr, "turn 1 failed: agent_error boom\n") {
		t.Fatalf("stderr = %q, want the failure line first and nothing before it", stderr)
	}
}

// TestIsTTYRefusesEverythingButATerminal pins the predicate's two rejections.
// A *bytes.Buffer fails the type assertion, which is what makes every command
// test above non-TTY; os.DevNull is a real *os.File and still not a terminal,
// on all three platforms.
func TestIsTTYRefusesEverythingButATerminal(t *testing.T) {
	if isTTY(&bytes.Buffer{}) {
		t.Fatal("a bytes.Buffer reported as a terminal")
	}
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if isTTY(f) {
		t.Fatalf("%s reported as a terminal", os.DevNull)
	}
}

// TestNewTurnSpinnerIsOffWithoutATerminal covers the constructor's two gates
// against a cobra command wired the way the tests above wire it.
func TestNewTurnSpinnerIsOffWithoutATerminal(t *testing.T) {
	for _, json := range []bool{false, true} {
		cmd := &cobra.Command{Use: "send"}
		jsonFlag(cmd)
		if json {
			if err := cmd.Flags().Set("json", "true"); err != nil {
				t.Fatalf("set --json: %v", err)
			}
		}
		cmd.SetErr(&bytes.Buffer{})
		if s := newTurnSpinner(cmd, time.Now()); s.on {
			t.Fatalf("the indicator is on with json=%v and a buffer for stderr", json)
		}
	}
}

// TestTurnSpinnerDrawsErasesAndClearsBeforeTheAnswer is the frame-writing
// itself, with the TTY predicate forced true. It pins the three properties the
// terminal depends on: the line is redrawn in place, a shrinking clock leaves
// no tail behind, and the erase precedes whatever is written next.
func TestTurnSpinnerDrawsErasesAndClearsBeforeTheAnswer(t *testing.T) {
	var buf bytes.Buffer
	start := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	s := &turnSpinner{w: &buf, on: true, start: start}

	s.draw(start.Add(2 * time.Minute))
	long := buf.String()
	if !strings.HasPrefix(long, "\r") || !strings.Contains(long, "working… 2m00s") {
		t.Fatalf("first frame = %q, want a carriage return and the label", long)
	}
	if s.frame != 1 {
		t.Fatalf("frame counter = %d after one draw, want 1", s.frame)
	}

	buf.Reset()
	s.draw(start.Add(14 * time.Second))
	short := buf.String()
	if !strings.Contains(short, "working… 14s") {
		t.Fatalf("second frame = %q, want the shorter label", short)
	}
	if !strings.HasSuffix(short, "  ") {
		t.Fatalf("a shorter label left its tail on screen: %q", short)
	}

	buf.Reset()
	s.erase()
	erased := buf.String()
	if !strings.HasPrefix(erased, "\r") || !strings.HasSuffix(erased, "\r") ||
		strings.TrimSpace(erased) != "" {
		t.Fatalf("erase wrote %q, want a blanked line between carriage returns", erased)
	}
	// Idempotent: the deferred erase after an explicit one writes nothing, so
	// a cleared line cannot be blanked twice into the answer's first row.
	buf.Reset()
	s.erase()
	if buf.String() != "" {
		t.Fatalf("a second erase wrote %q", buf.String())
	}

	// And off, it writes nothing at all — the redirected case, at the level
	// of the writer rather than the command.
	buf.Reset()
	off := &turnSpinner{w: &buf, on: false, start: start}
	off.draw(start.Add(time.Second))
	off.erase()
	if buf.String() != "" {
		t.Fatalf("a suppressed indicator wrote %q", buf.String())
	}
}

// TestChatSendPollAndFrameCadences pins the two intervals against each other:
// the frame must move several times inside one poll window, or the screen
// reads as frozen between polls — which is the whole feature.
func TestChatSendPollAndFrameCadences(t *testing.T) {
	if tui.SpinnerTick >= chatPollInterval {
		t.Fatalf("frame tick %v is not faster than the poll %v", tui.SpinnerTick, chatPollInterval)
	}
	if chatPollInterval/tui.SpinnerTick < 3 {
		t.Fatalf("only %d frames per poll window; the indicator would read as static",
			chatPollInterval/tui.SpinnerTick)
	}
}

// The --message-file tests (task 124.5, issue #501). The flag exists because
// a shell rewrites `/name` and expands `$name` in argv without a word, so what
// is asserted is the body the stub decoded: the bytes that reach the daemon,
// not the bytes the test handed the command.

// messageFileProbe is content no argv would carry intact through every shell:
// a leading `/` (Git Bash), a `$name` (bash, zsh and pwsh in double quotes), a
// double quote and an embedded newline — and it ends in "\n", which must
// arrive as well, because nothing is trimmed (task 124 decision 29).
const messageFileProbe = "/review $name \"quoted\"\nsecond line\n"

// TestChatSendMessageFileIsSentByteForByte: through a path and through `-`,
// the message the daemon receives is the input exactly, trailing newline and
// all. A leading BOM is valid UTF-8 and is not stripped (decision 32).
func TestChatSendMessageFileIsSentByteForByte(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, content string
		viaStdin      bool
	}{
		{"file", messageFileProbe, false},
		{"stdin", messageFileProbe, true},
		{"bom", "\uFEFF/x hi", false},
		{"whitespace only", "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, log := recordingChatStub(t, "done", "the answer")
			path, stdin := "-", tc.content
			if !tc.viaStdin {
				path, stdin = filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".txt"), ""
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatalf("write message file: %v", err)
				}
			}
			stdout, stderr, code := runChatSendStdin(t, h, stdin,
				"chat", "send", "3", "--message-file", path)
			if code != 0 {
				t.Fatalf("exit code %d, want 0 (stderr %q)", code, stderr)
			}
			if stdout != "the answer\n" {
				t.Errorf("stdout = %q, want the answer alone", stdout)
			}
			_, _, sent := log.snapshot()
			if len(sent) != 1 || sent[0] != tc.content {
				t.Fatalf("the daemon received %q, want exactly %q", sent, tc.content)
			}
		})
	}
}

// TestChatStartMessageFileSendsTheFirstMessage: `chat start --message-file`
// creates the chat and sends the file's bytes as its first message, the way
// --message sends its value.
func TestChatStartMessageFileSendsTheFirstMessage(t *testing.T) {
	h, log := recordingChatStub(t, "done", "the answer")
	stdout, stderr, code := runChatSendStdin(t, h, messageFileProbe,
		"chat", "start", "talk", "--project", "1", "--message-file", "-")
	if code != 0 {
		t.Fatalf("exit code %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "chat 3  talk") || !strings.HasSuffix(stdout, "the answer\n") {
		t.Errorf("stdout = %q, want the chat line then the answer", stdout)
	}
	_, created, sent := log.snapshot()
	if created != 1 {
		t.Errorf("chats created = %d, want 1", created)
	}
	if len(sent) != 1 || sent[0] != messageFileProbe {
		t.Fatalf("the daemon received %q, want exactly %q", sent, messageFileProbe)
	}
}

// TestChatMessageSourceIsExactlyOne holds decision 30: send takes the
// argument or the file and refuses both and neither; start's two flags are
// mutually exclusive. Every refusal is local, exits 1 and makes no request —
// on start, that means no chat.
func TestChatMessageSourceIsExactlyOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "message.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write message file: %v", err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"send with both", []string{"chat", "send", "3", "hello", "--message-file", path}, "not both"},
		{"send with neither", []string{"chat", "send", "3"}, "a message is required"},
		{
			"start with both",
			[]string{"chat", "start", "talk", "--project", "1", "--message", "hi", "--message-file", path},
			"[message message-file] were all set",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, log := recordingChatStub(t, "done", "the answer")
			_, stderr, code := runChatSend(t, h, tc.args...)
			if code != 1 {
				t.Errorf("exit code %d, want 1 (stderr %q)", code, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to say %q", stderr, tc.want)
			}
			if requests, _, _ := log.snapshot(); requests != 0 {
				t.Errorf("%d requests reached the daemon, want none", requests)
			}
		})
	}
}

// TestChatMessageFileRefusals: input the CLI cannot send byte for byte, or
// has no message in it, is refused before any request on both commands
// (decisions 32–34), so a refused `chat start` leaves no chat behind. No
// error quotes the content.
func TestChatMessageFileRefusals(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-message.txt")
	for _, tc := range []struct {
		name, path, stdin, want string
	}{
		{"invalid UTF-8", "-", "secret\xff\xfe tail", "not valid UTF-8"},
		{"empty", "-", "", "is empty"},
		{"missing file", missing, "", "no-such-message.txt"},
	} {
		for _, verb := range []string{"send", "start"} {
			t.Run(verb+" "+tc.name, func(t *testing.T) {
				args := []string{"chat", "send", "3", "--message-file", tc.path}
				if verb == "start" {
					args = []string{"chat", "start", "talk", "--project", "1", "--message-file", tc.path}
				}
				h, log := recordingChatStub(t, "done", "the answer")
				_, stderr, code := runChatSendStdin(t, h, tc.stdin, args...)
				if code != 1 {
					t.Errorf("exit code %d, want 1 (stderr %q)", code, stderr)
				}
				if !strings.Contains(stderr, "--message-file") || !strings.Contains(stderr, tc.want) {
					t.Errorf("stderr = %q, want the flag named and %q", stderr, tc.want)
				}
				if strings.Contains(stderr, "secret") {
					t.Errorf("the error echoes the content: %q", stderr)
				}
				if requests, created, _ := log.snapshot(); requests != 0 || created != 0 {
					t.Errorf("%d requests and %d chats created, want none", requests, created)
				}
			})
		}
	}
}

// TestChatMessageFileBound: the read is capped at maxInputFileBytes, the same
// bound and the same shape as --fields-file's (decisions 31 and 37). Exactly
// the bound is read and sent — the send route's own 64 KiB tier is the
// daemon's to enforce, and this stub has none — and one byte more is refused
// with the limit named and no request made.
func TestChatMessageFileBound(t *testing.T) {
	h, log := recordingChatStub(t, "done", "the answer")
	fit := strings.Repeat("x", maxInputFileBytes)
	if _, stderr, code := runChatSendStdin(t, h, fit,
		"chat", "send", "3", "--message-file", "-"); code != 0 {
		t.Fatalf("a message exactly at the bound: exit code %d (stderr %q)", code, stderr)
	}
	if _, _, sent := log.snapshot(); len(sent) != 1 || len(sent[0]) != maxInputFileBytes {
		t.Fatalf("a message exactly at the bound did not arrive whole")
	}

	h, log = recordingChatStub(t, "done", "the answer")
	_, stderr, code := runChatSendStdin(t, h, fit+"x",
		"chat", "send", "3", "--message-file", "-")
	if code != 1 {
		t.Errorf("one byte over the bound: exit code %d, want 1", code)
	}
	if !strings.Contains(stderr, "--message-file") ||
		!strings.Contains(stderr, strconv.Itoa(maxInputFileBytes)) {
		t.Errorf("stderr = %q, want the flag and the limit named", stderr)
	}
	if strings.Contains(stderr, "xxxx") {
		t.Errorf("the error echoes the content")
	}
	if requests, _, _ := log.snapshot(); requests != 0 {
		t.Errorf("%d requests reached the daemon, want none", requests)
	}
}
