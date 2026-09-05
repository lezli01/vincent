package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	var polls atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.URL.Path == "/v1/chats/3/send" && r.Method == http.MethodPost:
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
	}
}

// runChatSend runs the command against a stub daemon with stdout and stderr
// kept apart, which is the whole question here.
func runChatSend(t *testing.T, h http.HandlerFunc, args ...string) (stdout, stderr string, code int) {
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
	root.SetIn(strings.NewReader(""))
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
