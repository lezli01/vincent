package issuesource

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// TestReasonsAreGitHubs pins the seam's vocabulary to the strings the sync
// row, the outbox and the API wire carried before it existed (task 142
// decision 2): introducing the seam changes no value a client sees.
func TestReasonsAreGitHubs(t *testing.T) {
	for seam, gh := range map[string]string{
		ReasonDisabled:     github.ReasonDisabled,
		ReasonRateLimited:  github.ReasonRateLimited,
		ReasonNoCredential: github.ReasonNoCredential,
		ReasonUnauthorized: github.ReasonUnauthorized,
		ReasonForbidden:    github.ReasonForbidden,
		ReasonNoWriteScope: github.ReasonNoWriteScope,
		ReasonNotFound:     github.ReasonNotFound,
		ReasonGone:         github.ReasonGone,
		ReasonMoved:        github.ReasonMoved,
		ReasonBadRequest:   github.ReasonBadRequest,
		ReasonUnreachable:  github.ReasonUnreachable,
	} {
		if seam != gh {
			t.Errorf("seam reason %q != github's %q", seam, gh)
		}
	}
	if ProviderGitHub != "github" {
		t.Errorf("ProviderGitHub = %q", ProviderGitHub)
	}
}

// TestClassifyIsReasonOf: GitHub's classification is github.ReasonOf's,
// with the reset and the location carried through, for every reason and
// for an error github did not produce.
func TestClassifyIsReasonOf(t *testing.T) {
	reset := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	g := NewGitHub(nil, nil)
	cases := []error{
		nil,
		errors.New("dial tcp: no route"),
		&github.Error{Reason: github.ReasonRateLimited, ResetAt: reset},
		fmt.Errorf("wrapped: %w", &github.Error{Reason: github.ReasonRateLimited, ResetAt: reset}),
		&github.Error{Reason: github.ReasonMoved, Location: "https://api.github.com/repos/o/x/issues/9"},
		&github.Error{Reason: github.ReasonTimeout},
		&github.Error{Reason: github.ReasonBadResponse},
	}
	for _, r := range []string{
		github.ReasonNoCredential, github.ReasonUnauthorized, github.ReasonForbidden, github.ReasonNoWriteScope,
		github.ReasonNotFound, github.ReasonGone, github.ReasonBadRequest, github.ReasonUnreachable,
	} {
		cases = append(cases, &github.Error{Reason: r})
	}
	for _, err := range cases {
		f := g.Classify(err)
		if f.Reason != github.ReasonOf(err) {
			t.Errorf("Classify(%v).Reason = %q, ReasonOf = %q", err, f.Reason, github.ReasonOf(err))
		}
		var gerr *github.Error
		if errors.As(err, &gerr) {
			if !f.ResetAt.Equal(gerr.ResetAt) || f.Location != gerr.Location {
				t.Errorf("Classify(%v) = %+v, want reset %v location %q", err, f, gerr.ResetAt, gerr.Location)
			}
		} else if !f.ResetAt.IsZero() || f.Location != "" {
			t.Errorf("Classify(%v) = %+v, want no reset or location", err, f)
		}
	}
}

// TestLocate: a GitHub remote is addressable only with an owner/name and an
// issue number — the guards the sweep and the outbox applied before the seam.
func TestLocate(t *testing.T) {
	g := NewGitHub(nil, nil)
	for _, c := range []struct {
		repo   string
		number int
		ok     bool
	}{
		{"octo/repo", 7, true},
		{"octo/repo", 0, false},
		{"octo", 7, false},
		{"octo/repo/x", 7, false},
		{"/repo", 7, false},
		{"", 7, false},
	} {
		b, ok := g.Locate(store.IssueRemote{Repo: c.repo, Number: c.number})
		if ok != c.ok {
			t.Errorf("Locate(%q, %d) ok = %v, want %v", c.repo, c.number, ok, c.ok)
		}
		if ok && (b.String() != c.repo || repoOf(b) != (github.Repo{Owner: "octo", Name: "repo"})) {
			t.Errorf("Locate(%q, %d) = %q, %+v", c.repo, c.number, b.String(), repoOf(b))
		}
	}
}

// TestIssueOfMapsGitHub: the mapping the importer and the outbox shared
// before the seam — closed reasons, the compared state value, the kept
// remote JSON — now lives here.
func TestIssueOfMapsGitHub(t *testing.T) {
	updated := time.Date(2026, 9, 1, 1, 0, 0, 0, time.FixedZone("x", 3600))
	is := issueOf(&github.Issue{
		NodeID: "I_1", Number: 1, Repo: "o/r", Title: "t", State: github.StateClosed,
		StateReason: github.IssueReasonReopened, Assignee: "hubot", UpdatedAt: updated, Comments: 3,
	})
	if is.Remote.Provider != ProviderGitHub || is.Remote.RemoteKey != "I_1" || is.Remote.Number != 1 {
		t.Errorf("remote: %+v", is.Remote)
	}
	if !is.Closed() || is.Remote.CloseReason != issuestate.Completed {
		t.Errorf("a closed row reading reopened closes completed: %+v", is.Remote)
	}
	if want := (store.IssueStateValue{State: "closed", Reason: "completed"}); is.StateValue != want {
		t.Errorf("state value = %+v, want %+v", is.StateValue, want)
	}
	if !is.UpdatedAt().Equal(updated) || is.UpdatedAt().Location() != time.UTC || is.Comments != 3 {
		t.Errorf("updated %v, comments %d", is.UpdatedAt(), is.Comments)
	}
	if is.Remote.RemoteJSON != `{"state":"closed","assignees":["hubot"],"state_reason":"reopened"}` {
		t.Errorf("remote json = %s", is.Remote.RemoteJSON)
	}
	open := issueOf(&github.Issue{NodeID: "I_2", State: github.StateOpen, StateReason: "reopened"})
	if open.Closed() || open.StateValue != (store.IssueStateValue{State: "open"}) || open.UpdatedAt() != (time.Time{}) {
		t.Errorf("open: %+v", open)
	}
}
