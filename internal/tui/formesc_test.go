package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func yesKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'y', Text: "y"} }

func typeInForm(update func(tea.KeyPressMsg), text string) {
	for _, r := range text {
		update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// #1053: in the repair form, esc in the prompt keeps what was typed, and esc
// on the form asks before dropping it.
func TestRepairFormEscKeepsAndAsks(t *testing.T) {
	f := newRepairForm(7, "check_failed", "build")
	press := func(m tea.KeyPressMsg) (tea.Cmd, bool) { return f.update(m, nil) }
	f.startPrompt()
	typeInForm(func(m tea.KeyPressMsg) { press(m) }, "fix the test")
	press(escKey())
	if f.editing || f.prompt != "fix the test" {
		t.Fatalf("esc in the prompt lost it: editing %v, prompt %q", f.editing, f.prompt)
	}
	if _, exit := press(escKey()); exit || !f.confirming {
		t.Fatal("esc on a touched repair form closed it without asking")
	}
	if !strings.Contains(f.render(80, 40), "discard this draft? y/n") {
		t.Error("the question is not on screen")
	}
	if _, exit := press(tea.KeyPressMsg{Code: 'n', Text: "n"}); exit || f.confirming {
		t.Fatal("n did not keep the form")
	}
	press(escKey())
	if _, exit := press(yesKey()); !exit {
		t.Fatal("y did not close the form")
	}
	untouched := newRepairForm(7, "check_failed", "build")
	if _, exit := untouched.update(escKey(), nil); !exit {
		t.Fatal("esc on an untouched form did not close it")
	}
}

// #1053: the follow-up form, the same way.
func TestFollowUpFormEscKeepsAndAsks(t *testing.T) {
	f := newFollowUpForm(7, 1, "done")
	f.startBody()
	typeInForm(func(m tea.KeyPressMsg) { f.update(m, nil) }, "rerun it")
	f.update(escKey(), nil)
	if f.editing || f.body() != "rerun it" {
		t.Fatalf("esc in the body lost it: editing %v, body %q", f.editing, f.body())
	}
	if _, exit := f.update(escKey(), nil); exit || !f.confirming {
		t.Fatal("esc on a touched follow-up closed it without asking")
	}
	if _, exit := f.update(yesKey(), nil); !exit {
		t.Fatal("y did not close the form")
	}
}

// #1053: the pull request form's body editor keeps the edit on esc, as the
// issue form's body does, and the form asks before dropping it.
func TestCreatePRFormEscKeepsAndAsks(t *testing.T) {
	f, err := newCreatePRForm(7, "https://github.com/o/r/compare/main...feat?expand=1&title=T&body=B", "feat")
	if err != nil {
		t.Fatal(err)
	}
	f.cursor = cprBody
	f.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !f.editing {
		t.Fatal("enter did not open the body")
	}
	typeInForm(func(m tea.KeyPressMsg) { f.update(m) }, " and more")
	f.update(escKey())
	if f.editing || !strings.HasSuffix(f.body, " and more") {
		t.Fatalf("esc in the body lost the edit: editing %v, body %q", f.editing, f.body)
	}
	if _, exit := f.update(escKey()); exit || !f.confirming {
		t.Fatal("esc on a touched pull request form closed it without asking")
	}
	if _, exit := f.update(yesKey()); !exit {
		t.Fatal("y did not close the form")
	}
}

// #1053: the new-chat form asks before dropping a typed first message.
func TestNewChatFormEscAsks(t *testing.T) {
	f := newNewChatForm(nil, 1)
	typeInForm(func(m tea.KeyPressMsg) { f.update(m, nil) }, "hello")
	if _, done := f.update(escKey(), nil); done || !f.confirming {
		t.Fatal("esc on a typed new chat closed it without asking")
	}
	if _, done := f.update(yesKey(), nil); !done {
		t.Fatal("y did not close the form")
	}
	if _, done := newNewChatForm(nil, 1).update(escKey(), nil); !done {
		t.Fatal("esc on an untouched new chat did not close it")
	}
}
