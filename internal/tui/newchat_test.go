package tui

import (
	"errors"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestNewChatFormCapturesEveryRow holds task 067 decision 6: an open new-chat
// draft owns the keyboard on all six rows, not only the four text ones.
//
// PR L's rule — a form captures input "only while a text row is in edit
// mode" — is unchanged everywhere else; this is the one recorded exception,
// because the picker rows sit in the middle of a live draft and `q` on them
// quit the TUI with it (issue #279).
func TestNewChatFormCapturesEveryRow(t *testing.T) {
	f := newNewChatForm(nil, 7)
	for i := range newChatFields {
		f.focus = i
		if !f.capturesInput() {
			t.Errorf("focus %d does not capture input; a global `q` would quit the TUI with the draft", i)
		}
	}
}

// TestNewChatFormAgentPickerOffersOnlyResumers holds decision row 29 in the
// picker: only an adapter that can resume may hold a chat, so the form does
// not offer one the daemon is certain to refuse. An adapter whose daemon sent
// no `supports_resume` is not judged — nothing is filtered out on the
// strength of a field that was never sent.
func TestNewChatFormAgentPickerOffersOnlyResumers(t *testing.T) {
	f := newNewChatForm(nil, 7)
	f.applyFields(newChatFieldsMsg{agents: []apiclient.Agent{
		{Name: "codex", SupportsResume: ptr(false)},
		{Name: "claude", SupportsResume: ptr(true)},
		{Name: "cursor", SupportsResume: ptr(false)},
		{Name: "elder", SupportsResume: nil},
	}})
	var got []string
	for _, a := range f.agents {
		got = append(got, a.Name)
	}
	if len(got) != 2 || got[0] != "claude" || got[1] != "elder" {
		t.Fatalf("the picker offers %v, want claude and the unjudged elder adapter", got)
	}
	if name := f.agentName(); name != "claude" {
		t.Errorf("the form defaults to %q, want the first adapter that can resume", name)
	}
}

// TestNewChatFormSurfacesAFailedListing holds the other half of the wire
// issue #279 broke: a `GET /v1/projects` that failed must say so on the form,
// rather than leaving an empty picker that is indistinguishable from a
// working one.
func TestNewChatFormSurfacesAFailedListing(t *testing.T) {
	v := chatsFixture()
	v.create = newNewChatForm(nil, 0)
	v.update(newChatFieldsMsg{err: errors.New("the daemon is not answering")})
	if v.create == nil {
		t.Fatal("a failed listing closed the form")
	}
	if v.create.err == "" {
		t.Fatal("the form shows nothing after the projects listing failed")
	}
}

// TestChatsBoardDropsFieldsForAClosedForm holds the discard rule: a draft
// abandoned before its fetch landed must not be resurrected by it.
func TestChatsBoardDropsFieldsForAClosedForm(t *testing.T) {
	v := chatsFixture()
	v.update(newChatFieldsMsg{projects: []apiclient.Project{{ID: 1, Name: "one"}}})
	if v.create != nil {
		t.Fatal("a late newChatFieldsMsg reopened a discarded draft")
	}
}

// TestChatsBoardRefusesAChatWithNoProject holds the dead end shut: `n` on an
// installation with nothing registered says so on the board instead of
// opening a form that `ctrl+s` can only answer with `pick a project` and no
// field will accept one (issue #279).
func TestChatsBoardRefusesAChatWithNoProject(t *testing.T) {
	v := newChatsView()
	v.applyLoaded(chatsLoadedMsg{names: map[int64]string{}, projectsListed: true})
	v.updateKey(registryKey(t, "n"))
	if v.create != nil {
		t.Fatal("n opened a new-chat form with no project to create in")
	}
	if v.note == "" || !v.noteBad {
		t.Fatalf("the board says %q, want a refusal naming the missing project", v.note)
	}

	// A board that has not listed the projects yet knows nothing, and a
	// listing that failed knows nothing either: both open the form as before.
	unknown := newChatsView()
	unknown.applyLoaded(chatsLoadedMsg{names: map[int64]string{}})
	if _, cmd := unknown.updateKey(registryKey(t, "n")); cmd != nil {
		drain(cmd)
	}
	if unknown.create == nil {
		t.Fatal("n refused the form on a board that never listed the projects")
	}
}
