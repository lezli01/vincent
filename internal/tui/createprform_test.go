package tui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// compareURLFixture is what GET /v1/tasks/{id}/github/pull serves for a task
// with a branch and no link — the daemon's own construction, ampersand and
// all.
const compareURLFixture = "https://github.com/octo/api/compare/master...vincent/7-add-a-thing" +
	"?body=Add+a+thing%0A%0ACloses+%23231&expand=1&title=Add+a+thing"

func createPRFixture(t *testing.T) *createPRForm {
	t.Helper()
	f, err := newCreatePRForm(7, compareURLFixture, "vincent/7-add-a-thing")
	if err != nil {
		t.Fatalf("newCreatePRForm: %v", err)
	}
	return f
}

// The prefill arrives encoded in the URL's query and lands in the form's own
// rows: 052 decision 4 requires it be visible and editable before it is used.
func TestCreatePRFormDecodesThePrefill(t *testing.T) {
	f := createPRFixture(t)
	if f.title != "Add a thing" {
		t.Errorf("title = %q, want the decoded prefill", f.title)
	}
	if f.body != "Add a thing\n\nCloses #231" {
		t.Errorf("body = %q, want the decoded prefill", f.body)
	}
	if !strings.Contains(f.render(80, 20), "Closes #231") {
		t.Error("the body is not on screen before it is used")
	}
}

// An edit changes the URL that is opened — which is the whole point of
// editing it, and the thing handing GitHub the unedited URL could not do.
func TestCreatePRFormEditChangesTheOpenedURL(t *testing.T) {
	opened := withFakeOpener(t, nil)
	f := createPRFixture(t)
	f.setRow(cprTitle, "A better title")
	f.setRow(cprBody, "Rewritten body")
	drain(f.open())
	if len(*opened) != 1 {
		t.Fatalf("opened %v, want one URL", *opened)
	}
	u, err := url.Parse((*opened)[0])
	if err != nil {
		t.Fatalf("the rebuilt URL does not parse: %v", err)
	}
	q := u.Query()
	if q.Get("title") != "A better title" {
		t.Errorf("title = %q, want the edited one", q.Get("title"))
	}
	if q.Get("body") != "Rewritten body" {
		t.Errorf("body = %q, want the edited one", q.Get("body"))
	}
	// expand=1 is what makes GitHub open the form rather than the diff; the
	// rebuild must not drop what the daemon set.
	if q.Get("expand") != "1" {
		t.Errorf("expand = %q, want the daemon's 1", q.Get("expand"))
	}
	if u.Path != "/octo/api/compare/master...vincent/7-add-a-thing" {
		t.Errorf("path = %q, want the daemon's compare path", u.Path)
	}
}

// An emptied row drops its parameter rather than sending an empty one.
func TestCreatePRFormEmptyBodyDropsTheParameter(t *testing.T) {
	f := createPRFixture(t)
	f.setRow(cprBody, "  ")
	u, err := url.Parse(f.url())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := u.Query()["body"]; ok {
		t.Error("an emptied body is still encoded in the URL")
	}
}

// A title is required here rather than in a browser tab: GitHub's form is
// unusable without one, and finding that out over there is a round trip
// wasted.
func TestCreatePRFormRefusesAnEmptyTitle(t *testing.T) {
	opened := withFakeOpener(t, nil)
	f := createPRFixture(t)
	f.setRow(cprTitle, "")
	if cmd := f.open(); cmd != nil {
		t.Fatal("an empty title was opened")
	}
	if f.err == "" {
		t.Error("nothing said why")
	}
	if len(*opened) != 0 {
		t.Fatalf("the opener was called with %v", *opened)
	}
}

// The acceptance criterion stated as a test rather than left to inspection:
// the whole editor path contacts nothing. Mirrors internal/github's
// TestCompareURLMakesNoRequest — vincent still writes nothing at GitHub, and
// decision record row 11 stands.
// The form contacts GitHub directly from the client: never (task 069).
//
// The claim this test makes changed when the form's primary action did. It
// used to be "nothing is sent from here at all"; ctrl+s now posts to the
// **daemon**, which pushes the branch and creates the pull request. What
// still holds — and is the ownership invariant, not a nicety — is that the
// client itself makes no request to GitHub: every byte that reaches
// github.com is the daemon's.
//
// So this asserts two things. ctrl+s goes to the injected submit and nowhere
// else, and the fallback path still opens a URL and sends nothing.
func TestCreatePRFormNeverReachesGitHubDirectly(t *testing.T) {
	var reached []string
	prev := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		reached = append(reached, r.URL.Host)
		return nil, errNoOpener
	})
	t.Cleanup(func() { http.DefaultTransport = prev })
	withFakeOpener(t, nil)

	f := createPRFixture(t)
	var submitted int
	f.submit = func(string, string, bool) tea.Cmd {
		submitted++
		return nil
	}
	f.update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	f.setRow(cprBody, "edited")

	// ctrl+s: the daemon call, made through the injected seam.
	drain(f.send())
	if submitted != 1 {
		t.Fatalf("ctrl+s made %d daemon calls, want 1", submitted)
	}
	// The fallback: a browser hand-off, and still nothing sent.
	f.sending = false
	drain(f.open())
	_ = f.render(80, 20)
	if len(reached) > 0 {
		t.Fatalf("the pull-request form reached %v itself; every GitHub call is the daemon's", reached)
	}
}

// A second ctrl+s while the first is in flight is refused at the client
// (task 069 decision 7). The daemon refuses it too — the link is written the
// moment the pull request exists, so the second call sees a live link and is
// 409'd — and GitHub refuses a third for the same head and base.
func TestCreatePRFormRefusesDoubleSubmit(t *testing.T) {
	withFakeOpener(t, nil)
	f := createPRFixture(t)
	var submitted int
	f.submit = func(string, string, bool) tea.Cmd {
		submitted++
		return nil
	}
	drain(f.send())
	drain(f.send())
	if submitted != 1 {
		t.Fatalf("the form submitted %d times, want 1", submitted)
	}
	// A refusal from the daemon re-arms it: a title GitHub would not take is
	// a thing a human fixes and sends again.
	f.failed("GitHub refused these values for a pull request")
	drain(f.send())
	if submitted != 2 {
		t.Fatalf("the form did not re-arm after a failure: %d submissions", submitted)
	}
}

// The draft toggle is space on its own row, and it is what reaches the
// daemon as `draft`.
func TestCreatePRFormDraftToggle(t *testing.T) {
	withFakeOpener(t, nil)
	f := createPRFixture(t)
	var got bool
	f.submit = func(_, _ string, draft bool) tea.Cmd {
		got = draft
		return nil
	}
	f.cursor = cprDraft
	f.update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if !f.draft {
		t.Fatal("space did not toggle the draft row")
	}
	drain(f.send())
	if !got {
		t.Fatal("the draft flag did not reach the daemon call")
	}
	// The row says which state it is in, because a toggle nobody can read is
	// a guess.
	if v := f.rowValue(cprDraft); v != "draft" {
		t.Fatalf("draft row renders %q", v)
	}
	f.update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if v := f.rowValue(cprDraft); v != "ready for review" {
		t.Fatalf("un-drafted row renders %q", v)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// esc leaves the draft behind and ctrl+o hands off to the browser; both
// close the popup, which is what lets the task view drop it. ctrl+s no longer
// does (task 069): the daemon's answer has to have somewhere to land, and a
// form that vanished before it arrived would have nowhere to report a
// failure.
func TestCreatePRFormClosesOnEscAndBrowserHandoff(t *testing.T) {
	withFakeOpener(t, nil)
	f := createPRFixture(t)
	if _, exit := f.update(tea.KeyPressMsg{Code: tea.KeyEscape}); !exit {
		t.Error("esc did not close the form")
	}
	f = createPRFixture(t)
	if _, exit := f.update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}); !exit {
		t.Error("ctrl+o did not close the form")
	}
}
