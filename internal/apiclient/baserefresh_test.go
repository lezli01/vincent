package apiclient

import (
	"encoding/json"
	"testing"
)

const baseRefreshSample = `{
  "base_branch": "master",
  "base_sha": "abc1234def5678abc1234def5678abc1234def56",
  "base_refresh": {
    "fetch": {"result": "fetched", "remote": "origin", "ref": "refs/heads/master"},
    "fast_forward": {"result": "skipped", "reason": "checkout_dirty", "worktree": "/repos/api", "error": "local changes"}
  }
}`

func wantBaseRefreshSample(t *testing.T, got *BaseRefresh) {
	t.Helper()
	want := BaseRefresh{
		Fetch: BaseFetch{Result: "fetched", Remote: "origin", Ref: "refs/heads/master"},
		FastForward: BaseFastForward{
			Result: "skipped", Reason: "checkout_dirty", Worktree: "/repos/api", Error: "local changes",
		},
	}
	if got == nil {
		t.Fatal("base_refresh decoded as nil")
	}
	if *got != want {
		t.Errorf("base_refresh = %+v, want %+v", *got, want)
	}
}

func TestBaseRefreshDecodesOnTaskDetailAndChat(t *testing.T) {
	var task TaskDetail
	if err := json.Unmarshal([]byte(baseRefreshSample), &task); err != nil {
		t.Fatal(err)
	}
	if task.BaseSHA != "abc1234def5678abc1234def5678abc1234def56" {
		t.Errorf("task base_sha = %q", task.BaseSHA)
	}
	wantBaseRefreshSample(t, task.BaseRefresh)

	var chat Chat
	if err := json.Unmarshal([]byte(baseRefreshSample), &chat); err != nil {
		t.Fatal(err)
	}
	if chat.BaseSHA != "abc1234def5678abc1234def5678abc1234def56" {
		t.Errorf("chat base_sha = %q", chat.BaseSHA)
	}
	wantBaseRefreshSample(t, chat.BaseRefresh)
}

// A worktree not created yet, or created before the record existed, has no
// refresh: absent and null both decode as nil, never as a zero refresh that
// would read like an unknown result.
func TestBaseRefreshAbsentIsNil(t *testing.T) {
	for _, body := range []string{
		`{"base_branch": "master"}`,
		`{"base_branch": "master", "base_refresh": null}`,
	} {
		var task TaskDetail
		if err := json.Unmarshal([]byte(body), &task); err != nil {
			t.Fatal(err)
		}
		if task.BaseRefresh != nil || task.BaseSHA != "" {
			t.Errorf("%s: base_sha = %q, base_refresh = %+v, want empty and nil", body, task.BaseSHA, task.BaseRefresh)
		}
		var chat Chat
		if err := json.Unmarshal([]byte(body), &chat); err != nil {
			t.Fatal(err)
		}
		if chat.BaseRefresh != nil {
			t.Errorf("%s: chat base_refresh = %+v, want nil", body, chat.BaseRefresh)
		}
	}
}

func TestBaseRefreshWarning(t *testing.T) {
	fetched := BaseFetch{Result: "fetched", Remote: "origin", Ref: "refs/heads/master"}
	skipped := func(reason string) *BaseRefresh {
		return &BaseRefresh{Fetch: fetched, FastForward: BaseFastForward{Result: "skipped", Reason: reason}}
	}
	for _, tc := range []struct {
		name    string
		refresh *BaseRefresh
		want    string
	}{
		{name: "nil", refresh: nil, want: ""},
		{
			name: "fetch error",
			refresh: &BaseRefresh{
				Fetch: BaseFetch{
					Result: "error", Remote: "origin", Ref: "refs/heads/master",
					Error: "could not resolve host",
				},
				FastForward: BaseFastForward{Result: "not_attempted"},
			},
			want: "fetch from origin refs/heads/master failed: could not resolve host; base may be stale",
		},
		{
			name:    "fetch error without detail",
			refresh: &BaseRefresh{Fetch: BaseFetch{Result: "error"}, FastForward: BaseFastForward{Result: "not_attempted"}},
			want:    "fetch failed; base may be stale",
		},
		{name: "skipped diverged", refresh: skipped("diverged"), want: "local master not fast-forwarded: diverged"},
		{name: "skipped local_ahead", refresh: skipped("local_ahead"), want: "local master not fast-forwarded: local_ahead"},
		{name: "skipped checkout_dirty", refresh: skipped("checkout_dirty"), want: "local master not fast-forwarded: checkout_dirty"},
		{name: "skipped checkout_busy", refresh: skipped("checkout_busy"), want: "local master not fast-forwarded: checkout_busy"},
		{
			name: "skipped error with worktree",
			refresh: &BaseRefresh{Fetch: fetched, FastForward: BaseFastForward{
				Result: "skipped", Reason: "error", Worktree: "/repos/api", Error: "cannot lock ref",
			}},
			want: "local master not fast-forwarded: error in /repos/api: cannot lock ref",
		},
		{
			name:    "advanced",
			refresh: &BaseRefresh{Fetch: fetched, FastForward: BaseFastForward{Result: "advanced", Worktree: "/repos/api"}},
			want:    "",
		},
		{name: "up_to_date", refresh: &BaseRefresh{Fetch: fetched, FastForward: BaseFastForward{Result: "up_to_date"}}, want: ""},
		{
			name:    "not_attempted",
			refresh: &BaseRefresh{Fetch: BaseFetch{Result: "no_upstream"}, FastForward: BaseFastForward{Result: "not_attempted"}},
			want:    "",
		},
		{
			name:    "disabled",
			refresh: &BaseRefresh{Fetch: BaseFetch{Result: "disabled"}, FastForward: BaseFastForward{Result: "not_attempted"}},
			want:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.refresh.Warning(); got != tc.want {
				t.Errorf("Warning() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBaseDisplay(t *testing.T) {
	for _, tc := range []struct {
		branch, sha, want string
	}{
		{branch: "master", sha: "abc1234def5678abc1234def5678abc1234def56", want: "master @ abc1234"},
		{branch: "master", sha: "", want: "master"},
		{branch: "", sha: "", want: ""},
	} {
		got := TaskDetail{BaseBranch: tc.branch, BaseSHA: tc.sha}.BaseDisplay()
		if got != tc.want {
			t.Errorf("BaseDisplay(%q, %q) = %q, want %q", tc.branch, tc.sha, got, tc.want)
		}
	}
}
