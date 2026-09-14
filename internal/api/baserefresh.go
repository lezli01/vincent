package api

import "github.com/lezli01/vincent/internal/store"

// baseRefreshBody is what refreshing a task's or a chat's base did when its
// worktree was created (§10, §13.2, task 099): the fetch of the base branch
// from its upstream, and the fast-forward of the project's local base branch
// that follows it. It renders as null — never omitted — for a row created
// before the record existed, and for one whose worktree was never created, so
// a client can tell "nothing was recorded" from "the field is not served".
//
// It is a server DTO rather than store.BaseRefresh with tags, the way every
// other response type here is, so the store's column encoding and the wire
// shape can move independently.
type baseRefreshBody struct {
	Fetch       baseFetchBody       `json:"fetch"`
	FastForward baseFastForwardBody `json:"fast_forward"`
}

// baseFetchBody is the fetch half. Result is fetched, no_upstream, error or
// disabled; error means the worktree started from the local ref, which may be
// stale.
type baseFetchBody struct {
	Result string `json:"result"`
	Remote string `json:"remote,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Error  string `json:"error,omitempty"`
}

// baseFastForwardBody is the fast-forward half. Result is advanced,
// up_to_date, skipped or not_attempted; Reason says why a skip happened.
type baseFastForwardBody struct {
	Result   string `json:"result"`
	Reason   string `json:"reason,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Error    string `json:"error,omitempty"`
}

// renderBaseRefresh converts the stored record; nil stays nil.
func renderBaseRefresh(r *store.BaseRefresh) *baseRefreshBody {
	if r == nil {
		return nil
	}
	return &baseRefreshBody{
		Fetch: baseFetchBody{
			Result: r.Fetch.Result, Remote: r.Fetch.Remote, Ref: r.Fetch.Ref, Error: r.Fetch.Error,
		},
		FastForward: baseFastForwardBody{
			Result: r.FastForward.Result, Reason: r.FastForward.Reason,
			Worktree: r.FastForward.Worktree, Error: r.FastForward.Error,
		},
	}
}
