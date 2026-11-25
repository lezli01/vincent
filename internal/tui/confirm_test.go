package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// confirmKey is the press a terminal delivers for one answer key.
func confirmKey(s string) tea.KeyPressMsg {
	if s == "esc" {
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

// confirmSite is one y/n question in the TUI, raised and ready: press
// answers it with one key, open reports whether it is still up, and acted
// whether the thing it asked about was done.
type confirmSite struct {
	press func(key string)
	open  func() bool
	acted func() bool
}

// confirmSites raises every y/n question the shared rule covers (#1162),
// each fresh per call so one key's answer never leaks into the next.
var confirmSites = map[string]func(t *testing.T) confirmSite{
	"action bar": func(*testing.T) confirmSite {
		a := &actionBar{pending: apiclient.ActionCancel, pendingID: 3}
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { cmd, _ = a.handleKey(k, offlineClient(), taskActions{id: 3, state: "running"}) },
			open:  func() bool { return a.pending != "" },
			acted: func() bool { return cmd != nil },
		}
	},
	"diagnose": func(t *testing.T) confirmSite {
		a := &actionBar{}
		target := taskActions{id: 7, state: stateBlocked, actions: []string{apiclient.ActionChat}}
		if cmd, handled := diagnoseCmd(target, a); !handled || cmd != nil || a.pending != actionDiagnose {
			t.Fatal("Q did not ask first")
		}
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { cmd, _ = a.handleKey(k, offlineClient(), target) },
			open:  func() bool { return a.pending != "" },
			acted: func() bool { return cmd != nil },
		}
	},
	"daemon restart": func(*testing.T) confirmSite {
		d := requestDaemon()
		pressRestart(d) // the question, with its service reading landed (#1160)
		return confirmSite{
			press: func(k string) { d.update(confirmKey(k)) },
			open:  func() bool { return d.restartAsk },
			acted: func() bool { return d.restarting },
		}
	},
	"compare keep": func(*testing.T) confirmSite {
		v := compareFixture()
		v.client = offlineClient()
		v.updateKey(confirmKey("]"))
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { _, cmd = v.updateKey(confirmKey(k)) },
			open:  func() bool { return v.confirm >= 0 },
			acted: func() bool { return cmd != nil },
		}
	},
	"chat close": func(*testing.T) confirmSite {
		v := chatViewFixture()
		v.client = offlineClient()
		v.closing = true
		return chatSite(v, func() bool { return v.closing })
	},
	"chat compact": func(*testing.T) confirmSite {
		v := chatViewFixture()
		v.client = offlineClient()
		v.compactCmds = map[string]string{"claude": "/compact"}
		v.compacting = true
		return chatSite(v, func() bool { return v.compacting })
	},
	"chat raise": func(*testing.T) confirmSite {
		v := chatViewFixture()
		v.client = offlineClient()
		v.raising = true
		return chatSite(v, func() bool { return v.raising })
	},
	"chat clear queue": func(*testing.T) confirmSite {
		v := chatViewFixture()
		v.client = offlineClient()
		v.chat.QueuedCount = 2
		v.updateKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModAlt})
		return chatSite(v, func() bool { return v.clearingQueue })
	},
	"chat hunk reject": func(*testing.T) confirmSite {
		v := chatViewFixture()
		v.client = offlineClient()
		v.diff.open, v.diff.rejectAsk = true, &diffHunkRef{path: "a.go", id: "h1"}
		return chatSite(v, func() bool { return v.diff.rejectAsk != nil })
	},
	"chats archive": func(*testing.T) confirmSite {
		v := newChatsView()
		v.client = offlineClient()
		v.confirm = &archivePrompt{id: 1}
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { cmd = v.updateConfirm(confirmKey(k)) },
			open:  func() bool { return v.confirm != nil },
			acted: func() bool { return cmd != nil },
		}
	},
	"chats delete": func(*testing.T) confirmSite {
		v := newChatsView()
		v.client = offlineClient()
		v.delPrompt = &rowDeletePrompt{ids: []int64{1}, chats: true}
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { cmd = v.answerDelete(k) },
			open:  func() bool { return v.delPrompt != nil },
			acted: func() bool { return cmd != nil },
		}
	},
	"board delete": func(*testing.T) confirmSite {
		b := newBoard()
		b.client = offlineClient()
		b.delPrompt = &rowDeletePrompt{ids: []int64{1}}
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { cmd = b.answerDelete(k) },
			open:  func() bool { return b.delPrompt != nil },
			acted: func() bool { return cmd != nil },
		}
	},
	"project delete": func(*testing.T) confirmSite {
		p := newProjectsView()
		p.client = offlineClient()
		p.confirm = &deletePrompt{id: 1, name: "p"}
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { _, cmd = p.updateConfirm(confirmKey(k)) },
			open:  func() bool { return p.confirm != nil },
			acted: func() bool { return cmd != nil },
		}
	},
	"trigger write": func(*testing.T) confirmSite {
		v := newTriggersView()
		ran := false
		v.confirm = &triggerConfirm{question: "enable?", run: func() tea.Cmd { ran = true; return nil }}
		return confirmSite{
			press: func(k string) { v.updateConfirm(confirmKey(k)) },
			open:  func() bool { return v.confirm != nil },
			acted: func() bool { return ran },
		}
	},
	"pull request unlink": func(*testing.T) confirmSite {
		v := newPullRequestsView()
		v.client = offlineClient()
		v.confirm = &unlinkPrompt{taskID: 1}
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { _, cmd = v.updateConfirmKey(confirmKey(k)) },
			open:  func() bool { return v.confirm != nil },
			acted: func() bool { return cmd != nil },
		}
	},
	"workflow editor remove": func(*testing.T) confirmSite {
		removed := false
		var o wfEditorOverlay = newWFEditorConfirm("remove step 2?", func() tea.Cmd { removed = true; return nil })
		return confirmSite{
			press: func(k string) { o, _ = o.Update(confirmKey(k)) },
			open:  func() bool { return o != nil },
			acted: func() bool { return removed },
		}
	},
	"settings dangerous key": func(*testing.T) confirmSite {
		f := newConfigForm(configKeys()[0], apiclient.Config{Listen: "127.0.0.1:0"})
		f.confirming = true
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { cmd, _ = f.update(confirmKey(k), offlineClient()) },
			open:  func() bool { return f.confirming },
			acted: func() bool { return cmd != nil },
		}
	},
	"issue delete": func(*testing.T) confirmSite {
		a := newIssueDelete(offlineClient(), apiclient.Issue{ID: 1, Title: "x"})
		var cmd tea.Cmd
		done := false
		return confirmSite{
			press: func(k string) { cmd, done = a.updateKey(confirmKey(k)) },
			open:  func() bool { return !done && a.stage == iaConfirm },
			acted: func() bool { return cmd != nil },
		}
	},
	"new task discard": func(*testing.T) confirmSite {
		n := newNewTask()
		n.mode = ntConfirming
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { _, cmd = n.updateConfirm(confirmKey(k)) },
			open:  func() bool { return n.mode == ntConfirming },
			acted: func() bool { return cmd != nil },
		}
	},
	"follow-up discard": func(*testing.T) confirmSite {
		f := newFollowUpForm(1, 1, "")
		f.confirming = true
		exit := false
		return confirmSite{
			press: func(k string) { _, exit = f.update(confirmKey(k), nil) },
			open:  func() bool { return f.confirming },
			acted: func() bool { return exit },
		}
	},
	"repair discard": func(*testing.T) confirmSite {
		f := newRepairForm(1, "", "")
		f.confirming = true
		exit := false
		return confirmSite{
			press: func(k string) { _, exit = f.update(confirmKey(k), nil) },
			open:  func() bool { return f.confirming },
			acted: func() bool { return exit },
		}
	},
	"new chat discard": func(*testing.T) confirmSite {
		f := newNewChatForm(nil, 1)
		f.confirming = true
		exit := false
		return confirmSite{
			press: func(k string) { _, exit = f.update(confirmKey(k), nil) },
			open:  func() bool { return f.confirming },
			acted: func() bool { return exit },
		}
	},
	"leave guard": func(t *testing.T) confirmSite {
		m := phaseRoot(t, phaseConnected, 80, 24)
		m.leave = &pendingLeave{draft: "task draft", quit: true}
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { cmd = m.updatePendingLeaveKey(confirmKey(k)) },
			open:  func() bool { return m.leave != nil },
			acted: func() bool { return cmd != nil },
		}
	},
	"project switch": func(t *testing.T) confirmSite {
		m := phaseRoot(t, phaseConnected, 80, 24)
		m.pending = &pendingSwitch{project: apiclient.Project{ID: 2, Name: "other"}, draft: "task draft"}
		var cmd tea.Cmd
		return confirmSite{
			press: func(k string) { cmd = m.updatePendingSwitchKey(confirmKey(k)) },
			open:  func() bool { return m.pending != nil },
			acted: func() bool { return cmd != nil },
		}
	},
	"global pause": func(t *testing.T) confirmSite {
		m := phaseRoot(t, phaseConnected, 80, 24)
		m.globalPause.asking = true
		return confirmSite{
			press: func(k string) { m.updatePauseAskKey(confirmKey(k)) },
			open:  func() bool { return m.globalPause.asking },
			acted: func() bool { return m.globalPause.busy },
		}
	},
}

// chatSite is a chat workspace's question, answered through its keyboard.
func chatSite(v *chatView, open func() bool) confirmSite {
	var cmd tea.Cmd
	return confirmSite{
		press: func(k string) {
			if v.diff.open {
				cmd = v.updateDiffKey(confirmKey(k))
				return
			}
			_, cmd = v.updateKey(confirmKey(k))
		},
		open:  open,
		acted: func() bool { return cmd != nil },
	}
}

// TestConfirmationsAnswerTheSameWay is #1162: every y/n question takes y or
// Y as yes and n, N or esc as no, and any other key leaves it up having done
// nothing.
func TestConfirmationsAnswerTheSameWay(t *testing.T) {
	cases := []struct {
		key         string
		open, acted bool
	}{
		{"y", false, true},
		{"Y", false, true},
		{"n", false, false},
		{"N", false, false},
		{"esc", false, false},
		{"x", true, false},
		{"q", true, false},
	}
	for name, arm := range confirmSites {
		for _, c := range cases {
			t.Run(name+"/"+c.key, func(t *testing.T) {
				s := arm(t)
				if !s.open() {
					t.Fatal("the question is not up before the key")
				}
				s.press(c.key)
				if got := s.open(); got != c.open {
					t.Errorf("after %q the question is up=%v, want %v", c.key, got, c.open)
				}
				if got := s.acted(); got != c.acted {
					t.Errorf("after %q it acted=%v, want %v", c.key, got, c.acted)
				}
			})
		}
	}
}

// TestConfirmAnswerIsTheOneRule pins the helper itself.
func TestConfirmAnswerIsTheOneRule(t *testing.T) {
	for key, want := range map[string][2]bool{
		"y": {true, false}, "Y": {true, false},
		"n": {false, true}, "N": {false, true}, "esc": {false, true},
		"enter": {false, false}, "q": {false, false}, "x": {false, false}, " ": {false, false},
	} {
		yes, no := confirmAnswer(key)
		if yes != want[0] || no != want[1] {
			t.Errorf("confirmAnswer(%q) = %v, %v; want %v, %v", key, yes, no, want[0], want[1])
		}
	}
}

// TestDiagnoseAndClearQueueAskFirst is #1162's second decision: `Q` and
// alt+c no longer act on the key; each raises a question that leads with its
// answer keys and names what a yes starts or loses.
func TestDiagnoseAndClearQueueAskFirst(t *testing.T) {
	a := &actionBar{}
	cmd, handled := diagnoseCmd(taskActions{id: 7, state: stateBlocked, actions: []string{apiclient.ActionChat}}, a)
	if !handled || cmd != nil {
		t.Fatalf("Q acted on the key: handled=%v cmd=%v", handled, cmd != nil)
	}
	if got := a.confirmPrompt(); !strings.HasPrefix(got, "diagnose #7? (y/n) · starts") {
		t.Errorf("the diagnose question is %q", got)
	}

	// A chat already open is reopened without asking: nothing new starts.
	b := &actionBar{}
	if cmd, _ := diagnoseCmd(taskActions{id: 7, state: stateBlocked, openChatID: 4}, b); cmd == nil || b.pending != "" {
		t.Error("Q over an open chat asked instead of reopening it")
	}

	v := chatViewFixture()
	v.client = offlineClient()
	v.chat.QueuedCount = 2
	if _, cmd := v.updateKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModAlt}); cmd != nil {
		t.Fatal("alt+c cleared the queue without asking")
	}
	if got := v.clearQueueQuestion(); got != "clear the queue? (y/n) · 2 queued messages are deleted unsent" {
		t.Errorf("the clear question is %q", got)
	}
	if keys := v.promptKeys(); len(keys) < 4 || keys[0] != "y" || keys[2] != "n" {
		t.Errorf("the footer offers %q while the clear question is up", keys)
	}
	v.chat.QueuedCount = 0
	v.clearingQueue = false
	v.updateKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModAlt})
	if v.clearingQueue || v.note != "nothing is queued" {
		t.Errorf("alt+c over an empty queue: asking=%v note=%q", v.clearingQueue, v.note)
	}
}
