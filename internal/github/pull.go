package github

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// PullRequest is the normalized GitHub pull request — the *only* shape either
// leg produces (task 052, spec §5.3, §13.2).
//
// Unlike Issue it is never snapshotted onto a task. What a task stores is a
// PullLink: a repo and a number. Everything below is re-read on every render,
// because draft, state and merged status are live by nature and a stored copy
// of them would read exactly like a current one while being wrong.
type PullRequest struct {
	// Repo is `owner/name`, recorded so a row identifies itself the way an
	// Issue snapshot does.
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	// Body is the pull request description, normalized by both legs the way
	// Issue.Body already is (task 064). It exists so a task created from a
	// pull request starts with what the pull request says, and for nothing
	// else: it is never stored on the task as a re-renderable snapshot.
	Body string `json:"body,omitempty"`
	URL  string `json:"url"`
	// State is StateOpen or StateClosed. A merged pull request is closed and
	// carries Merged; `gh` spells that third state MERGED and the REST API
	// does not spell it at all, so it is folded onto a bool here rather than
	// becoming a third State value only one leg can produce.
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Merged bool   `json:"merged"`
	// HeadBranch is the branch the pull request is *from*. It is the ground
	// truth the task link is reconciled against: vincent names every task's
	// branch itself, so a head branch equal to `tasks.branch_name` is the one
	// match rule that does not need the task to have come from an issue.
	HeadBranch string `json:"head_branch,omitempty"`
	// HeadRepo is `owner/name` of the repository the head branch lives in
	// (task 064). It is what makes a fork *detectable*: a fork's head branch
	// does not exist in the base repository at all, so a task created from
	// one has to fetch `refs/pull/{n}/head` and can never push back. Empty
	// when the leg that answered did not report it — a deleted fork reports
	// no head repository at all — which reads as "same repository", the
	// answer that was implied everywhere before this field existed.
	HeadRepo   string `json:"head_repo,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
	Author     string `json:"author,omitempty"`

	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// FetchedAt is when this was read. It is the honest answer to "how old is
	// this?" for a value that has no snapshot to be older than.
	FetchedAt time.Time `json:"fetched_at,omitzero"`
}

// Zero reports that no pull request is described.
func (p PullRequest) Zero() bool { return p.Number == 0 }

// Fork reports that the head branch lives in a different repository from the
// pull request itself (task 064 decision 5). An empty HeadRepo is *not* a
// fork: a leg that did not report one, or a head repository that has since
// been deleted, must read as the same repository rather than silently turning
// an ordinary pull request into a no-upstream one.
func (p PullRequest) Fork() bool {
	return p.HeadRepo != "" && p.Repo != "" && !strings.EqualFold(p.HeadRepo, p.Repo)
}

// HeadFetchRef is the ref a task created from this pull request fetches
// (task 064 decision 2). A same-repository pull request fetches its head
// branch, which is a real branch on the remote and can carry an upstream; a
// fork's head does not exist in the base repository, so it is read through
// GitHub's own `refs/pull/{n}/head`, which is a read and nothing else — the
// daemon never runs `git remote add` for a fork (decision 5).
func (p PullRequest) HeadFetchRef() string {
	if p.Fork() {
		return fmt.Sprintf("refs/pull/%d/head", p.Number)
	}
	return "refs/heads/" + p.HeadBranch
}

// Status is the one word a row renders: merged wins over closed, and draft
// over open, because that is the order a human reads them in.
func (p PullRequest) Status() string {
	switch {
	case p.Merged:
		return "merged"
	case p.State == StateClosed:
		return StateClosed
	case p.Draft:
		return "draft"
	default:
		return StateOpen
	}
}

// sortPulls orders a listing newest first, mirroring sortIssues and for the
// same reason: the order is applied after parsing rather than trusted from
// the wire, so a listing whose order depends on which leg answered is not a
// difference a user can see (task 035 decision 1).
func sortPulls(pulls []PullRequest) {
	sort.SliceStable(pulls, func(a, b int) bool {
		ta, tb := pulls[a].CreatedAt, pulls[b].CreatedAt
		if ta.Equal(tb) {
			return pulls[a].Number > pulls[b].Number
		}
		return ta.After(tb)
	})
}

// PullLink is the durable half: what a task stores about its pull request
// (migration 0018, spec §5.3/§14). It is a **pointer**, never a snapshot.
//
// Repo rides beside Number because a number alone is meaningless — this is
// where task 035 decision 5's "repo identity is not stored" was revisited, as
// that decision predicted, and the identity landed on the task rather than as
// a `github_repo` column on the project.
type PullLink struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// Source is SourceAuto when the reconciler matched a head branch to this
	// task's branch, SourceHuman when a person said so. The reconciler never
	// overwrites a human link.
	Source string `json:"source"`
	// Suppressed is the sticky record of a human unlink. It is why the column
	// has three states and not two: "never matched", "linked" and
	// "matched, and a human said no" are different, and the absence of a link
	// cannot carry the third.
	Suppressed bool `json:"suppressed,omitempty"`
	// LinkedAt is when the link was last written, by either source.
	LinkedAt time.Time `json:"linked_at,omitzero"`
	// Branch records that this task's branch **is** the pull request's head
	// branch, because the task was created from it (task 064 decision 8).
	//
	// The link alone cannot carry that fact — a human may link any task to
	// any pull request — and three separate places need it: admission checks
	// out the head instead of cutting a branch (decision 2), archive skips
	// both branch legs because vincent did not cut this branch (decision 3),
	// and a retry refuses `branch_override` (decision 10).
	//
	// It rides in the existing `github_pull_json` envelope beside Source and
	// Suppressed, so it is a shape change and not a migration. It stores
	// nothing renderable, so 052's "a pull request is a pointer, never a
	// snapshot" is untouched.
	Branch bool `json:"branch,omitempty"`
	// Fork records that the head lives in another repository, so the branch
	// was fetched with no upstream and nothing can push back (decision 5).
	// Stated on the task at creation rather than discovered when a delivery
	// step fails.
	Fork bool `json:"fork,omitempty"`
}

// Link sources.
const (
	SourceAuto  = "auto"
	SourceHuman = "human"
)

// Linked reports that this task names a pull request. A suppressed row is a
// record of a refusal, not a link.
func (l *PullLink) Linked() bool { return l != nil && l.Number > 0 && !l.Suppressed }

// FromPull reports that this task's branch came from the pull request rather
// than being cut by vincent (task 064 decision 8). Suppression is deliberately
// not consulted: a human unlinking the pull request does not retroactively make
// vincent the author of the branch, and archive must still keep its hands off it.
func (l *PullLink) FromPull() bool { return l != nil && l.Number > 0 && l.Branch }

// CompareURL builds GitHub's "open a pull request" page for a branch, with
// the title and body prefilled as query parameters.
//
// **No request is made to GitHub here.** This is string construction over a
// Repo this package parsed and text the human just edited; it is the whole of
// what task 052 does in the direction of GitHub, and decision record row 11 —
// vincent pushes nothing, opens nothing, merges nothing — stands unamended. A
// human presses GitHub's own button.
func CompareURL(repo Repo, base, head, title, body string) string {
	q := url.Values{}
	q.Set("expand", "1")
	if title != "" {
		q.Set("title", title)
	}
	if body != "" {
		q.Set("body", body)
	}
	return fmt.Sprintf("https://%s/%s/%s/compare/%s...%s?%s",
		Host, url.PathEscape(repo.Owner), url.PathEscape(repo.Name),
		url.PathEscape(base), url.PathEscape(head), q.Encode())
}

// PullURL is the web page for one pull request, built from a parsed Repo and
// this package's own template rather than from any string GitHub returned.
// It is what the opener is handed when a listing row carries no URL of its
// own — a task's stored link has a number and nothing else.
func PullURL(repo Repo, number int) string {
	return fmt.Sprintf("https://%s/%s/%s/pull/%d",
		Host, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
}
