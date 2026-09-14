package apiclient

import "strings"

// BaseRefresh is what the daemon did to a task's or chat's base before cutting
// its branch (GitHub issue #430): fetch the base from its upstream, then try to
// fast-forward the human's local base branch to what was fetched. It is nil
// when the worktree has not been created yet, or was created by a daemon that
// predates the record. The task always starts at base_sha, whatever this says
// happened to the local branch.
type BaseRefresh struct {
	Fetch       BaseFetch       `json:"fetch"`
	FastForward BaseFastForward `json:"fast_forward"`
}

// BaseFetch is the fetch half. Result is one of fetched, no_upstream, error or
// disabled; error means the fetch failed and the task started from the local
// ref, which may be stale.
type BaseFetch struct {
	Result string `json:"result"`
	Remote string `json:"remote,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Error  string `json:"error,omitempty"`
}

// BaseFastForward is the fast-forward half. Result is one of advanced,
// up_to_date, skipped or not_attempted; skipped leaves the local base branch
// where it was, for Reason (diverged, local_ahead, checkout_dirty,
// checkout_busy, error). Worktree is the checkout of the local base branch,
// when it has one.
type BaseFastForward struct {
	Result   string `json:"result"`
	Reason   string `json:"reason,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Warning is the one line a client shows when the refresh degraded: the fetch
// failed, or the local base branch was left behind. It is "" for every other
// outcome, including a nil receiver, so a caller renders a row exactly when
// this is non-empty and never has to guard.
func (r *BaseRefresh) Warning() string {
	if r == nil {
		return ""
	}
	var parts []string
	if r.Fetch.Result == "error" {
		msg := "fetch"
		if r.Fetch.Remote != "" {
			msg += " from " + r.Fetch.Remote
		}
		if r.Fetch.Ref != "" {
			msg += " " + r.Fetch.Ref
		}
		msg += " failed"
		if r.Fetch.Error != "" {
			msg += ": " + r.Fetch.Error
		}
		parts = append(parts, msg+"; base may be stale")
	}
	if r.FastForward.Result == "skipped" {
		// The fetched ref names the branch on the remote, which is the local
		// base branch's name too: the refresh fetches the base by its own name.
		branch := strings.TrimPrefix(r.Fetch.Ref, "refs/heads/")
		if branch == "" {
			branch = "base"
		}
		msg := "local " + branch + " not fast-forwarded"
		if r.FastForward.Reason != "" {
			msg += ": " + r.FastForward.Reason
		}
		if r.FastForward.Worktree != "" {
			msg += " in " + r.FastForward.Worktree
		}
		if r.FastForward.Error != "" {
			msg += ": " + r.FastForward.Error
		}
		parts = append(parts, msg)
	}
	return strings.Join(parts, "; ")
}

// BaseDisplay is the task's starting point: `master @ abc1234` when the daemon
// recorded the commit the branch was cut from, or the branch name alone when
// it did not — the branch was cut from the local base.
func (t TaskDetail) BaseDisplay() string {
	return baseDisplay(t.BaseBranch, t.BaseSHA)
}

func baseDisplay(branch, sha string) string {
	if sha == "" {
		return branch
	}
	if len(sha) > 7 {
		sha = sha[:7]
	}
	if branch == "" {
		return sha
	}
	return branch + " @ " + sha
}
