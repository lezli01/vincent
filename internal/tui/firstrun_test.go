package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ackedDir is a data dir whose first-run notice is already acknowledged, so
// a test that is not about the notice does not have to dismiss it first.
func ackedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := acknowledgeNotice(dir); err != nil {
		t.Fatalf("acknowledgeNotice: %v", err)
	}
	return dir
}

// The whole point of the persisted flag: a second launch against the same
// data dir must not warn again.
func TestFirstRunNoticeShownOnceAcrossRestarts(t *testing.T) {
	dir := t.TempDir()

	first := newRoot(testCtx(t), fakeConnector(), dir)
	if !first.notice.active {
		t.Fatal("first launch did not show the notice")
	}
	if !strings.Contains(content(first), "Full-auto") {
		t.Error("the notice is not what the first frame renders")
	}
	first.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if first.notice.active {
		t.Fatal("enter did not dismiss the notice")
	}
	if first.notice.ackErr != nil {
		t.Fatalf("ack write failed: %v", first.notice.ackErr)
	}

	second := newRoot(testCtx(t), fakeConnector(), dir)
	if second.notice.active {
		t.Error("the notice came back on the second launch")
	}
}

// The flag is written on dismissal, never on display: a quit two seconds in
// must not permanently bury a security notice nobody read.
func TestFirstRunNoticeIsNotAcknowledgedByQuitting(t *testing.T) {
	dir := t.TempDir()

	m := newRoot(testCtx(t), fakeConnector(), dir)
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd == nil {
		t.Fatal("ctrl+c did not quit")
	}
	if noticeAcknowledged(dir) {
		t.Error("quitting recorded the acknowledgment")
	}
	if again := newRoot(testCtx(t), fakeConnector(), dir); !again.notice.active {
		t.Error("the notice did not come back after quitting through it")
	}
}

// esc and q are what people press to make a box go away without reading it.
// This is the one screen where that must not work.
func TestFirstRunNoticeSwallowsTheDismissKeys(t *testing.T) {
	dir := t.TempDir()
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyEsc},
		{Code: 'q', Text: "q"},
		{Code: '1', Text: "1"},
		{Code: 'n', Text: "n"},
		{Code: '?', Text: "?"},
	} {
		m := newRoot(testCtx(t), fakeConnector(), dir)
		m.Update(key)
		if !m.notice.active {
			t.Errorf("%q dismissed the notice", key.String())
		}
		if m.help {
			t.Errorf("%q reached the shell through the notice", key.String())
		}
	}
}

// Fail open: an unreadable or malformed flag file shows the warning again.
// Suppressing a security notice because a parse failed is the wrong
// direction to fail in.
func TestFirstRunNoticeFailsOpen(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(statePath(dir), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if noticeAcknowledged(dir) {
			t.Error("a malformed state file suppressed the notice")
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod does not deny reads on Windows")
		}
		if os.Geteuid() == 0 {
			t.Skip("root reads it anyway")
		}
		dir := t.TempDir()
		if err := acknowledgeNotice(dir); err != nil {
			t.Fatalf("acknowledgeNotice: %v", err)
		}
		if err := os.Chmod(statePath(dir), 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if noticeAcknowledged(dir) {
			t.Error("an unreadable state file suppressed the notice")
		}
	})
	t.Run("unresolved data dir", func(t *testing.T) {
		if noticeAcknowledged("") {
			t.Error("an unresolved data dir suppressed the notice")
		}
	})
}

// A write that fails still dismisses — the notice was read, which was the
// point — but it says so, because the next launch will show it again.
func TestFirstRunNoticeReportsAFailedWriteWithoutBlocking(t *testing.T) {
	m := newRoot(testCtx(t), fakeConnector(), "")

	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.notice.active {
		t.Fatal("a failed write kept the overlay up")
	}
	if m.notice.ackErr == nil {
		t.Fatal("a failed write was not reported")
	}
	line, ok := m.notice.statusLine()
	if !ok || !strings.Contains(line, "shown again") {
		t.Errorf("statusLine = %q, want it to say the notice comes back", line)
	}
	if !strings.Contains(content(m), "could not record") {
		t.Error("the failed write does not reach the screen")
	}
}

// A field this build does not know about must survive being read by it.
func TestAcknowledgeNoticeKeepsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(statePath(dir),
		[]byte(`{"some_future_pref":"keep me"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := acknowledgeNotice(dir); err != nil {
		t.Fatalf("acknowledgeNotice: %v", err)
	}
	b, err := os.ReadFile(statePath(dir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(b), "keep me") {
		t.Errorf("state = %s, want the unknown field preserved", b)
	}
	if !noticeAcknowledged(dir) {
		t.Error("the acknowledgment did not stick")
	}
}

func TestAcknowledgeNoticeCreatesTheDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-yet")
	if err := acknowledgeNotice(dir); err != nil {
		t.Fatalf("acknowledgeNotice: %v", err)
	}
	if !noticeAcknowledged(dir) {
		t.Error("the acknowledgment did not stick in a fresh data dir")
	}
}

// The notice has to name the risk, not gesture at it.
func TestNoticeNamesTheRisk(t *testing.T) {
	body := firstRunNotice{}.render()
	for _, want := range []string{"as your user", "restricted", "transcripted", "README"} {
		if !strings.Contains(body, want) {
			t.Errorf("the notice does not mention %q", want)
		}
	}
}
