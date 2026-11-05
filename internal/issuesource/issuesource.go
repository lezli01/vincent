package issuesource

import (
	"context"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The reason vocabulary the seam owns (task 142 decision 2). The strings
// are the ones the sync row, the outbox and the API wire have always
// carried — GitHub's own — so introducing the seam changes no value a
// client sees; a test pins each to its github.Reason* counterpart. A source
// maps its provider's failures onto these in Classify; a failure it cannot
// place is ReasonUnreachable, as GitHub's was.
const (
	ReasonDisabled     = "disabled"
	ReasonRateLimited  = "rate_limited"
	ReasonNoCredential = "no_credential" //nolint:gosec // G101: a reason, not a credential
	ReasonUnauthorized = "unauthorized"
	ReasonForbidden    = "forbidden"
	ReasonNoWriteScope = "no_write_scope"
	ReasonNotFound     = "not_found"
	ReasonGone         = "gone"
	ReasonMoved        = "moved"
	ReasonBadRequest   = "bad_request"
	ReasonUnreachable  = "unreachable"
)

// The Jira write-back's chooser reasons (task 142.13): a vincent close or
// reopen that no single available transition achieves exactly. Each is
// terminal — the row fails and the local state stays as the human set it —
// and a test pins each to its jiramap.Code* counterpart.
const (
	// ReasonNoTransition: no available transition achieves the request
	// (or Jira offered none, which a missing Transition issues permission
	// also causes).
	ReasonNoTransition = "no_transition"
	// ReasonAmbiguousTransition: several do, and vincent will not guess.
	ReasonAmbiguousTransition = "ambiguous_transition"
	// ReasonTransitionNeedsFields: the one that would needs a required
	// screen field vincent does not set.
	ReasonTransitionNeedsFields = "transition_needs_fields"
)

// Failure is a provider error as the sync's policy reads it.
type Failure struct {
	// Reason is the seam's vocabulary above, or a provider-specific string
	// the policy treats as transient (GitHub's timeout, bad_response).
	Reason string
	// ResetAt is when a rate_limited call may be tried again; zero when the
	// provider did not say.
	ResetAt time.Time
	// Location is where a moved issue went, when the provider said.
	Location string
}

// Binding is a project's place on a provider: for GitHub, a repository. Its
// String is the sticky key the sync row records (IssueSyncState.Repo) and
// the remote rows carry, so a project re-pointed elsewhere is noticed.
type Binding struct {
	key string
	ref any // the provider's own handle, opaque outside its source
}

// NewBinding is for a Source: key is the sticky string, ref whatever the
// source needs to address the provider again.
func NewBinding(key string, ref any) Binding { return Binding{key: key, ref: ref} }

// String is the binding's sticky key.
func (b Binding) String() string { return b.key }

// Ref is the provider handle NewBinding was given.
func (b Binding) Ref() any { return b.ref }

// ListOptions bound one issue listing.
type ListOptions struct {
	// OpenOnly lists open issues only; otherwise every state, because a
	// sync that asked for open issues only could not tell "closed" from
	// "never changed".
	OpenOnly bool
	// Since keeps issues updated at or after it; zero is no bound.
	Since time.Time
	// ETag is a previous Page's, for a conditional listing; a source
	// without conditional listings ignores it.
	ETag string
	// PageCap bounds the pages walked; <= 0 is the source's default.
	PageCap int
	// After is an opaque resume cursor a previous truncated Page returned
	// as ResumeAfter, for a source whose walks are ordered by id rather
	// than by update time (Jira's). Such a source's OpenOnly listing
	// ignores Since and resumes by After; its other listings resume a
	// truncated walk by After too, which then carries the walk's own bound
	// in place of Since. A source that resumes by time ignores After
	// (GitHub does).
	After string
}

// Issue is one provider issue, already mapped.
type Issue struct {
	// Remote is the issue as the store imports it. ProjectID is left zero
	// for the caller; Provider, RemoteKey, Repo and Number are the source's.
	Remote store.RemoteIssue
	// StateValue is the provider's state as the outbox compares it.
	StateValue store.IssueStateValue
	// Comments is how many comments the provider counts on the issue; zero
	// skips reading its thread when it is first imported.
	Comments int
}

// Closed reports whether the provider has the issue closed.
func (is *Issue) Closed() bool { return is.Remote.State == issuestate.Closed }

// UpdatedAt is the provider's last-update time, zero when it gave none.
func (is *Issue) UpdatedAt() time.Time {
	if is.Remote.RemoteUpdatedAt == nil {
		return time.Time{}
	}
	return *is.Remote.RemoteUpdatedAt
}

// Page is one issue listing, with GitHub's durable-listing rules: Unchanged
// is a conditional listing's "nothing moved", ETag is what to send next
// time, Truncated says PageCap stopped the walk, and ResumeSince is where to
// resume it from. ResumeAfter is set instead, when Truncated, by a source
// whose walks are ordered by id: the next listing's After.
type Page struct {
	Issues      []Issue
	Unchanged   bool
	ETag        string
	Truncated   bool
	ResumeSince time.Time
	ResumeAfter string
}

// Comment is one provider comment on an issue.
type Comment struct {
	// IssueNumber is the issue's number on a numbered provider, as a
	// repository-wide listing names it; zero otherwise.
	IssueNumber int
	// Remote is the comment as the store mirrors it.
	Remote store.RemoteIssueComment
}

// CommentQuery bounds one repository-wide comment listing.
type CommentQuery struct {
	Since time.Time
	ETag  string
}

// CommentPage is one repository-wide comment listing, with Page's rules.
type CommentPage struct {
	Comments    []Comment
	Unchanged   bool
	ETag        string
	Truncated   bool
	ResumeSince time.Time
}

// Source is one issue provider. Every method but Provider, Bind, Locate and
// Classify may make a network call; none keeps state between calls.
type Source interface {
	// Provider is the provider column's value on every row this source
	// writes: the sync row, the remote rows, the outbox's dispatch key.
	Provider() string
	// Bind derives project's binding at the point of use; ok is false when
	// the project is not on this provider.
	Bind(ctx context.Context, project store.Project) (b Binding, ok bool)
	// Locate is the binding rem lives at; ok is false when rem cannot be
	// addressed on this provider at all (for GitHub: no owner/name, or no
	// issue number).
	Locate(rem store.IssueRemote) (b Binding, ok bool)
	// ListIssues lists b's issues updated since opts.Since, oldest first.
	ListIssues(ctx context.Context, b Binding, opts ListOptions) (Page, error)
	// GetIssue reads rem's issue, at b. An issue that moved off b is an
	// error Classify maps to ReasonMoved with the new Location; a source
	// that can read it there (Jira) returns it too, fully mapped with its
	// NEW Repo, Number, RemoteKey and URL, and the importer rewrites the
	// remote row to them. GitHub returns a zero Issue there: a zero
	// Remote.Repo means no rewrite.
	GetIssue(ctx context.Context, b Binding, rem store.IssueRemote) (Issue, error)
	// ListComments reads is's whole thread, at b.
	ListComments(ctx context.Context, b Binding, is *Issue) ([]Comment, error)
	// SetIssueState writes v onto rem's issue, at b.
	SetIssueState(ctx context.Context, b Binding, rem store.IssueRemote, v store.IssueStateValue) error
	// CreateComment posts body on rem's issue, at b, and returns the posted
	// comment's remote key — the one ListComments will report it under.
	CreateComment(ctx context.Context, b Binding, rem store.IssueRemote, body string) (remoteKey string, err error)
	// Classify maps an error one of the calls above returned onto the
	// seam's vocabulary. A nil error is the zero Failure.
	Classify(err error) Failure
}

// RepoCommentLister is a source that lists every comment of a binding in
// one conditional listing (GitHub's repository-wide since+ETag). Without
// it, the importer reads the thread of each issue its pass listed.
type RepoCommentLister interface {
	ListRepoComments(ctx context.Context, b Binding, q CommentQuery) (CommentPage, error)
}

// PlaceholderAdopter is a source whose rows may be backfill placeholders
// (task 035, migration 0040; task 130 decision 22.3), adopted onto the
// listed issue with their number. AdoptsPlaceholders is the capability's
// marker and always reports true.
type PlaceholderAdopter interface {
	AdoptsPlaceholders() bool
}
