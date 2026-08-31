package github

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CheckRun is the normalized CI row for one commit — the *only* shape either
// leg produces, the way Issue and PullRequest already are (task 068, spec
// §5.3, §13.2).
//
// GitHub has two unrelated things a client calls "a check". A **check run**
// belongs to a GitHub App (Actions is one of them) and has a status and a
// conclusion; a **commit status** is the older API, has neither, and carries
// one `state` word instead. `gh pr view --json statusCheckRollup` folds both
// into one array and the REST leg reads them from two different endpoints, so
// the folding has to happen here or the two legs would disagree about what a
// pull request's checks even are.
//
// It is never snapshotted, for the reason PullRequest is not: a stored check
// result reads exactly like a current one while being wrong.
type CheckRun struct {
	// Name is the check's own name — a check run's `name`, a commit status's
	// `context`.
	Name string `json:"name"`
	// State is one of the CheckState constants: the single word a row
	// renders. A check run's status and conclusion are folded onto it, so
	// "queued" and "success" are the same kind of value and a client has one
	// field to switch on.
	State string `json:"state"`
	// URL is the check's own page on GitHub, empty when the check reported
	// none. It is whatever the far side said, so it is only ever handed to a
	// browser opener after HTTPURL has vetted it.
	URL string `json:"url,omitempty"`
	// RunID is the GitHub Actions workflow run this row belongs to, and 0
	// when the row is not Actions-backed (task 068 decision 3).
	//
	// This is the provenance both legs have to agree on, and agreeing is the
	// reason it is derived from the check's *URL* rather than from either
	// leg's own metadata: the REST leg could read `app.slug`, and the `gh`
	// leg has no app field at all. An Actions check's details URL is
	// `/{owner}/{repo}/actions/runs/{run}/job/{job}` on both legs, so parsing
	// it is the one rule that gives the same answer to "is this an Actions
	// run, and which one" whichever leg answered.
	RunID int64 `json:"run_id,omitempty"`

	StartedAt   time.Time `json:"started_at,omitzero"`
	CompletedAt time.Time `json:"completed_at,omitzero"`
}

// Check states. They are snake_case for the reason the block reasons and the
// unavailability reasons are: one vocabulary, spelled the same wherever it
// surfaces.
//
// GitHub's own conclusions are used as-is where they exist, because inventing
// synonyms for `timed_out` and `action_required` would lose the distinction a
// human needs to know whether to press re-run. The two states that are *not*
// conclusions — queued and in_progress — are the states a check has before it
// has one.
const (
	CheckQueued         = "queued"
	CheckInProgress     = "in_progress"
	CheckSuccess        = "success"
	CheckFailure        = "failure"
	CheckCancelled      = "cancelled"
	CheckSkipped        = "skipped"
	CheckNeutral        = "neutral"
	CheckTimedOut       = "timed_out"
	CheckActionRequired = "action_required"
	CheckStale          = "stale"
)

// Actions reports that this row is backed by a GitHub Actions workflow run,
// which is the only kind re-run can honestly be offered on (task 068
// decision 3): a third-party check run is owned by its own app, and a legacy
// commit status has no run behind it to re-run at all.
func (c CheckRun) Actions() bool { return c.RunID > 0 }

// Running reports that the check has not reached a conclusion yet.
func (c CheckRun) Running() bool {
	return c.State == CheckQueued || c.State == CheckInProgress
}

// Failed reports a conclusion a human would call a failure. `neutral`,
// `skipped` and `stale` are deliberately not failures — GitHub does not block
// a merge on them, and calling them red here would make the rollup disagree
// with the button it exists to inform.
func (c CheckRun) Failed() bool {
	switch c.State {
	case CheckFailure, CheckTimedOut, CheckActionRequired, CheckCancelled:
		return true
	default:
		return false
	}
}

// CheckRollup is every check on one commit, plus the commit it is about.
//
// The ref rides along because the answer is only meaningful against it: a
// pull request that gains a push while this was in flight has checks that
// belong to the *previous* head, and a client that renders them under the new
// one would be showing a green build for code nobody ran.
type CheckRollup struct {
	Ref   string     `json:"ref,omitempty"`
	Runs  []CheckRun `json:"runs"`
	State string     `json:"state"`
	// FetchedAt is when this was read — the honest answer to "how old is
	// this?" for a value that has no snapshot to be older than.
	FetchedAt time.Time `json:"fetched_at,omitzero"`
}

// rollupState is the one word for a whole commit: red if anything failed,
// yellow while anything is still running, green when everything that reached
// a conclusion passed, and empty when there are no checks at all.
//
// Failure wins over running on purpose. A pull request with one failed job
// and five still going is failed, and reporting it as in progress would hide
// the only fact worth acting on.
func rollupState(runs []CheckRun) string {
	if len(runs) == 0 {
		return ""
	}
	running := false
	for _, r := range runs {
		switch {
		case r.Failed():
			return CheckFailure
		case r.Running():
			running = true
		}
	}
	if running {
		return CheckInProgress
	}
	return CheckSuccess
}

// newRollup normalizes an ordered rollup out of whatever the leg collected.
func newRollup(ref string, runs []CheckRun, now time.Time) CheckRollup {
	sortChecks(runs)
	return CheckRollup{Ref: ref, Runs: runs, State: rollupState(runs), FetchedAt: now}
}

// sortChecks orders the rows the way sortPulls orders a listing, and for the
// same reason: the order is applied after parsing rather than trusted from
// the wire, so a list whose order depends on which leg answered is not a
// difference a user can see (task 035 decision 1).
//
// Unfinished first, then failed, then the rest, and by name inside each
// group. That is reading order for the question the tab exists to answer:
// what is still going, and what went wrong.
func sortChecks(runs []CheckRun) {
	sort.SliceStable(runs, func(a, b int) bool {
		ra, rb := checkRank(runs[a]), checkRank(runs[b])
		if ra != rb {
			return ra < rb
		}
		if runs[a].Name != runs[b].Name {
			return runs[a].Name < runs[b].Name
		}
		return runs[a].RunID < runs[b].RunID
	})
}

func checkRank(c CheckRun) int {
	switch {
	case c.Running():
		return 0
	case c.Failed():
		return 1
	default:
		return 2
	}
}

// normalizeCheckState folds a check run's status and conclusion onto one
// word. A completed run with no conclusion is neutral rather than a made-up
// state: GitHub does produce that pair, and it is not a failure.
func normalizeCheckState(status, conclusion string) string {
	if c := strings.ToLower(strings.TrimSpace(conclusion)); c != "" {
		switch c {
		case CheckSuccess, CheckFailure, CheckCancelled, CheckSkipped,
			CheckNeutral, CheckTimedOut, CheckActionRequired, CheckStale:
			return c
		case "startup_failure":
			return CheckFailure
		default:
			// An unknown conclusion is a failure rather than a state a client
			// has never seen: GitHub adds conclusions, and a new red one
			// rendering as green is the one wrong answer here.
			return CheckFailure
		}
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return CheckNeutral
	case "queued", "waiting", "requested", "pending", "":
		return CheckQueued
	default:
		return CheckInProgress
	}
}

// normalizeStatusState folds a legacy commit status's single `state` word
// onto the same vocabulary. `error` becomes failure: GitHub's own UI treats
// the two the same, and a seventh state only one leg can produce is exactly
// what this package exists to avoid.
func normalizeStatusState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "success":
		return CheckSuccess
	case "failure", "error":
		return CheckFailure
	default:
		return CheckInProgress
	}
}

// actionsRunPath matches the run id inside a GitHub Actions details URL,
// which is the shape both legs report for an Actions-backed check:
// https://github.com/{owner}/{repo}/actions/runs/{run}/job/{job}. Anchored on
// `/actions/runs/` so a third-party check whose URL merely mentions the word
// cannot be mistaken for one.
var actionsRunPath = regexp.MustCompile(`/actions/runs/([0-9]+)(?:/|$)`)

// actionsRunID extracts the workflow run id from a check's URL, and 0 when
// the URL is not one of GitHub's own Actions run pages (task 068 decision 3).
//
// The host is checked, not just the path: a check run may report any URL it
// likes, and a third-party service serving `/actions/runs/1` would otherwise
// hand vincent a run id to re-run on github.com.
func actionsRunID(raw string) int64 {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return 0
	}
	if !strings.EqualFold(u.Host, Host) && !strings.EqualFold(u.Host, "www."+Host) {
		return 0
	}
	m := actionsRunPath.FindStringSubmatch(u.Path)
	if m == nil {
		return 0
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0
	}
	return id
}
