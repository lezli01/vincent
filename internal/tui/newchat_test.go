package tui

import (
	"errors"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// chatFormWithCatalogs is the form as it stands once `GET /v1/projects` and
// `GET /v1/agents` have both landed: two projects with different default
// branches, and two resumable adapters with different catalogs — which is what
// makes "the catalogs scope to the selected adapter" observable.
func chatFormWithCatalogs() *newChatForm {
	f := newNewChatForm(nil, 0)
	f.applyFields(newChatFieldsMsg{
		projects: []apiclient.Project{
			{ID: 1, Name: "alpha", Path: "/repos/alpha", DefaultBranch: "main"},
			{ID: 2, Name: "beta", Path: "/repos/beta", DefaultBranch: "trunk"},
		},
		agents: []apiclient.Agent{
			{
				Name: "claude", Available: true, SupportsResume: ptr(true),
				DefaultModel:  "sonnet",
				DefaultEffort: "medium",
				Models: []apiclient.AgentOption{
					{Value: "sonnet", Source: apiclient.OptionSourceCLI},
					{Value: "opus", Source: apiclient.OptionSourceCurated},
				},
				Efforts: []apiclient.AgentOption{
					{Value: "medium", Source: apiclient.OptionSourceCurated},
					{Value: "high", Source: apiclient.OptionSourceCurated},
				},
			},
			{
				Name: "cursor", Available: true, SupportsResume: ptr(true),
				DefaultModel: "cursor-fast",
				Models: []apiclient.AgentOption{
					{Value: "cursor-fast", Source: apiclient.OptionSourceCLI},
				},
			},
		},
	})
	return f
}

// pickerLabels is what the open list offers, in order.
func pickerLabels(p *picker) []string {
	out := make([]string, 0, len(p.options))
	for _, o := range p.options {
		out = append(out, o.label)
	}
	return out
}

// openAt focuses a row and presses enter on it, the way a human reaches a
// list.
func openAt(t *testing.T, f *newChatForm, row ncRow) {
	t.Helper()
	f.focus = row
	if _, done := f.update(registryKey(t, "enter"), nil); done {
		t.Fatalf("enter on row %d closed the form", row)
	}
}

// TestNewChatFormCapturesEveryRow holds task 067 decision 8: an open new-chat
// draft owns the keyboard on all six rows, not only the two text ones, and
// with a list open on top of them.
//
// PR L's rule — a form captures input "only while a text row is in edit
// mode" — is unchanged everywhere else; this is the one recorded exception,
// because the list rows sit in the middle of a live draft and `q` on them
// quit the TUI with it (issue #279).
func TestNewChatFormCapturesEveryRow(t *testing.T) {
	f := newNewChatForm(nil, 7)
	for row := ncProject; row < ncRowCount; row++ {
		f.focus = row
		if !f.capturesInput() {
			t.Errorf("row %d does not capture input; a global `q` would quit the TUI with the draft", row)
		}
	}

	f = chatFormWithCatalogs()
	openAt(t, f, ncModel)
	if !f.capturesInput() {
		t.Error("an open list does not capture input, so `q` would quit the TUI with the draft under it")
	}
}

// TestNewChatFormAgentPickerOffersOnlyResumers holds decision row 29 in the
// picker: only an adapter that can resume may hold a chat, so the form does
// not offer one the daemon is certain to refuse. An adapter whose daemon sent
// no `supports_resume` is not judged — nothing is filtered out on the
// strength of a field that was never sent.
//
// The claim is made twice: on what the form holds, and on the list the human
// actually sees, which is the surface issue #281 moved it to.
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

	openAt(t, f, ncAgent)
	if f.pick == nil {
		t.Fatal("enter on the agent row opened no list")
	}
	if labels := pickerLabels(f.pick); len(labels) != 2 ||
		labels[0] != "claude" || labels[1] != "elder" {
		t.Fatalf("the agent list offers %v, want only the adapters that can resume", labels)
	}
	if f.pick.allowFree {
		t.Error("the agent list allows free text; an adapter vincent does not register cannot hold a chat")
	}
}

// TestNewChatFormProjectPickerOffersEveryProject holds the row the issue was
// filed about: the project row is a list of everything registered, with the
// path as its note, not a cycler you step through hoping.
func TestNewChatFormProjectPickerOffersEveryProject(t *testing.T) {
	f := chatFormWithCatalogs()
	openAt(t, f, ncProject)
	if f.pick == nil {
		t.Fatal("enter on the project row opened no list")
	}
	if labels := pickerLabels(f.pick); len(labels) != 2 ||
		labels[0] != "alpha" || labels[1] != "beta" {
		t.Fatalf("the project list offers %v, want both registered projects", labels)
	}
	if note := f.pick.options[0].note; note != "/repos/alpha" {
		t.Errorf("the first row's note is %q, want the project's path", note)
	}
	if f.pick.allowFree {
		t.Error("the project list allows free text; a chat needs a registered project")
	}

	// Choosing the second one commits it and closes the list.
	f.update(registryKey(t, "down"), nil)
	f.update(registryKey(t, "enter"), nil)
	if f.pick != nil {
		t.Fatal("choosing a project left the list open")
	}
	if f.projectID != 2 {
		t.Fatalf("the form holds project %d, want the one chosen from the list", f.projectID)
	}
}

// TestNewChatFormCatalogListsAreTheAdapters holds what the model and effort
// rows became: the selected adapter's catalog with its `cli`/`curated`
// provenance, led by an "(agent default)" row naming what the adapter would
// use, and with the free-text row present because a catalog is a suggestion
// (§9.6).
func TestNewChatFormCatalogListsAreTheAdapters(t *testing.T) {
	for _, tc := range []struct {
		name    string
		row     ncRow
		labels  []string
		notes   []string
		heading string
	}{
		{
			name: "model", row: ncModel, heading: "model",
			labels: []string{"(agent default)", "sonnet", "opus"},
			notes:  []string{"sonnet", apiclient.OptionSourceCLI, apiclient.OptionSourceCurated},
		},
		{
			name: "effort", row: ncEffort, heading: "effort",
			labels: []string{"(agent default)", "medium", "high"},
			notes:  []string{"medium", apiclient.OptionSourceCurated, apiclient.OptionSourceCurated},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := chatFormWithCatalogs()
			openAt(t, f, tc.row)
			if f.pick == nil {
				t.Fatalf("enter on the %s row opened no list", tc.name)
			}
			if f.pick.heading != tc.heading {
				t.Errorf("the list is headed %q, want %q", f.pick.heading, tc.heading)
			}
			if got := pickerLabels(f.pick); !equalStrings(got, tc.labels) {
				t.Fatalf("the %s list offers %v, want %v", tc.name, got, tc.labels)
			}
			for i, want := range tc.notes {
				if got := f.pick.options[i].note; got != want {
					t.Errorf("option %d is noted %q, want %q", i, got, want)
				}
			}
			if !f.pick.allowFree {
				t.Error("the list has no free-text row; a model shipped this morning is not in the catalog")
			}
		})
	}
}

// TestNewChatFormAgentChangeRescopesTheCatalogs holds the rule followUpForm
// states: the model and effort catalogs belong to an adapter, so keeping
// values chosen under another one would submit a pair that never existed.
func TestNewChatFormAgentChangeRescopesTheCatalogs(t *testing.T) {
	f := chatFormWithCatalogs()
	openAt(t, f, ncModel)
	f.update(registryKey(t, "down"), nil) // (agent default) → sonnet
	f.update(registryKey(t, "enter"), nil)
	f.model, f.effort = "sonnet", "high"

	// Stepping the agent row in place re-scopes both.
	f.focus = ncAgent
	f.update(registryKey(t, "right"), nil)
	if f.agentName() != "cursor" {
		t.Fatalf("→ on the agent row chose %q, want the next adapter", f.agentName())
	}
	if f.model != "" || f.effort != "" {
		t.Fatalf("the form still holds model %q effort %q chosen under the previous adapter", f.model, f.effort)
	}
	openAt(t, f, ncModel)
	if got := pickerLabels(f.pick); !equalStrings(got, []string{"(agent default)", "cursor-fast"}) {
		t.Fatalf("the model list offers %v, want the newly selected adapter's catalog", got)
	}
	if note := f.pick.options[0].note; note != "cursor-fast" {
		t.Errorf("the default row is noted %q, want the adapter's own default model", note)
	}

	// So does choosing one from the list.
	f.pick = nil
	f.model, f.effort = "cursor-fast", "high"
	openAt(t, f, ncAgent)
	// The list opens on the current adapter, so one step up reaches claude.
	f.update(registryKey(t, "up"), nil)
	f.update(registryKey(t, "enter"), nil)
	if f.agentName() != "claude" {
		t.Fatalf("the list chose %q, want claude", f.agentName())
	}
	if f.model != "" || f.effort != "" {
		t.Fatalf("choosing an adapter kept model %q effort %q from the previous one", f.model, f.effort)
	}
}

// TestNewChatFormArrowsStepInPlace holds the decision issue #281 took: `←`/`→`
// survive as a fast in-place step on the two short rows, the enum-row
// precedent from the new-task fields editor. They open nothing.
func TestNewChatFormArrowsStepInPlace(t *testing.T) {
	f := chatFormWithCatalogs()
	f.focus = ncProject
	f.update(registryKey(t, "right"), nil)
	if f.projectID != 2 || f.pick != nil {
		t.Fatalf("→ on the project row left project %d, list open = %v; want a step in place",
			f.projectID, f.pick != nil)
	}
	f.focus = ncAgent
	f.update(registryKey(t, "right"), nil)
	if f.agentName() != "cursor" || f.pick != nil {
		t.Fatalf("→ on the agent row left %q, list open = %v; want a step in place",
			f.agentName(), f.pick != nil)
	}
}

// TestNewChatFormBaseRowNamesTheProjectDefault holds the base row's decision:
// the placeholder names the project's real default branch and follows the
// project row, while the value stays empty so the daemon resolves it.
func TestNewChatFormBaseRowNamesTheProjectDefault(t *testing.T) {
	f := chatFormWithCatalogs()
	if got := f.base.Placeholder(); got != "main (the project's default)" {
		t.Fatalf("the base row reads %q, want the first project's default branch", got)
	}
	f.focus = ncProject
	f.update(registryKey(t, "right"), nil)
	if got := f.base.Placeholder(); got != "trunk (the project's default)" {
		t.Fatalf("after changing project the base row reads %q, want the new project's default branch", got)
	}

	f.title.SetValue("a chat")
	if req := f.request(); req.BaseBranch != "" {
		t.Fatalf("an untouched base row submits %q; it must stay empty so the daemon resolves the default",
			req.BaseBranch)
	}
	f.base.SetValue("  release  ")
	if req := f.request(); req.BaseBranch != "release" {
		t.Fatalf("a typed base row submits %q, want the branch that was typed", req.BaseBranch)
	}
}

// TestNewChatFormSubmitsFreeText holds `allowFree`: a value the adapter's
// catalog has never heard of is submitted verbatim. `POST /v1/chats` stores
// model and effort without a catalog check, so the form must not be stricter
// than the daemon.
func TestNewChatFormSubmitsFreeText(t *testing.T) {
	f := chatFormWithCatalogs()
	f.title.SetValue("a chat")
	openAt(t, f, ncModel)
	f.update(registryKey(t, "e"), nil) // the free-text row
	f.pick.input.SetValue("sonnet-shipped-this-morning")
	f.update(registryKey(t, "enter"), nil)
	if f.pick != nil {
		t.Fatal("committing free text left the list open")
	}
	if req := f.request(); req.Model != "sonnet-shipped-this-morning" {
		t.Fatalf("the form submits model %q, want the value typed into the free-text row", req.Model)
	}
}

// TestNewChatFormEscClosesOneLayer holds §15's esc stack: an open list is a
// layer above the form, so the first press closes the list and leaves the
// draft, and only the second discards it.
func TestNewChatFormEscClosesOneLayer(t *testing.T) {
	f := chatFormWithCatalogs()
	f.title.SetValue("still being written")
	openAt(t, f, ncModel)

	if _, done := f.update(registryKey(t, "esc"), nil); done {
		t.Fatal("esc with a list open closed the whole form, discarding the draft")
	}
	if f.pick != nil {
		t.Fatal("esc left the list open")
	}
	if f.title.Value() != "still being written" {
		t.Fatalf("the draft's title is now %q; closing a list must not touch it", f.title.Value())
	}
	if _, done := f.update(registryKey(t, "esc"), nil); !done {
		t.Fatal("a second esc did not close the form")
	}
}

// TestNewChatFormEnterNeverCreates holds the other half of that decision:
// `enter` opens a list or moves on, uniformly, and `ctrl+s` is the sole create
// key on every row — which is what §15 documents and what the form's own hint
// line has always said.
func TestNewChatFormEnterNeverCreates(t *testing.T) {
	f := chatFormWithCatalogs()
	f.title.SetValue("a chat")
	f.focus = ncTitle
	if cmd, _ := f.update(registryKey(t, "enter"), nil); cmd != nil {
		t.Fatal("enter on the title row produced a command; ctrl+s is what creates")
	}
	if f.submitting {
		t.Fatal("enter on the title row started a create")
	}
	if f.focus != ncAgent {
		t.Fatalf("enter on the title row left the cursor on row %d, want the next one", f.focus)
	}

	// ctrl+s from a row that is not the title still creates; with no client
	// the attempt stops at "not connected", which is proof it was made.
	for _, row := range []ncRow{ncProject, ncBase, ncEffort} {
		f.err = ""
		f.focus = row
		f.update(registryKey(t, "ctrl+s"), nil)
		if f.err != "not connected" {
			t.Errorf("ctrl+s on row %d did not attempt a create (err = %q)", row, f.err)
		}
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
