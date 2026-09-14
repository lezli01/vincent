package cli

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `task show`'s starting-point rows (issue #430): `base` always, and
// `refresh` exactly when the refresh degraded or left the local base behind.
func TestTaskBaseRows(t *testing.T) {
	const sha = "abc1234def5678abc1234def5678abc1234def56"
	fetched := apiclient.BaseFetch{Result: "fetched", Remote: "origin", Ref: "refs/heads/master"}
	for _, tc := range []struct {
		name    string
		sha     string
		refresh *apiclient.BaseRefresh
		want    [][2]string
	}{
		{
			name: "no base_sha, no refresh",
			want: [][2]string{{"base", "master"}},
		},
		{
			name: "fetch error",
			sha:  sha,
			refresh: &apiclient.BaseRefresh{
				Fetch: apiclient.BaseFetch{
					Result: "error", Remote: "origin", Ref: "refs/heads/master", Error: "could not resolve host",
				},
				FastForward: apiclient.BaseFastForward{Result: "not_attempted"},
			},
			want: [][2]string{
				{"base", "master @ abc1234"},
				{"refresh", "fetch from origin refs/heads/master failed: could not resolve host; base may be stale"},
			},
		},
		{
			name: "skipped fast-forward",
			sha:  sha,
			refresh: &apiclient.BaseRefresh{
				Fetch:       fetched,
				FastForward: apiclient.BaseFastForward{Result: "skipped", Reason: "checkout_dirty"},
			},
			want: [][2]string{
				{"base", "master @ abc1234"},
				{"refresh", "local master not fast-forwarded: checkout_dirty"},
			},
		},
		{
			name: "advanced",
			sha:  sha,
			refresh: &apiclient.BaseRefresh{
				Fetch: fetched, FastForward: apiclient.BaseFastForward{Result: "advanced"},
			},
			want: [][2]string{{"base", "master @ abc1234"}},
		},
		{
			name: "up_to_date",
			sha:  sha,
			refresh: &apiclient.BaseRefresh{
				Fetch: fetched, FastForward: apiclient.BaseFastForward{Result: "up_to_date"},
			},
			want: [][2]string{{"base", "master @ abc1234"}},
		},
		{
			name: "not_attempted",
			refresh: &apiclient.BaseRefresh{
				Fetch:       apiclient.BaseFetch{Result: "no_upstream"},
				FastForward: apiclient.BaseFastForward{Result: "not_attempted"},
			},
			want: [][2]string{{"base", "master"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := taskBaseRows(apiclient.TaskDetail{BaseBranch: "master", BaseSHA: tc.sha, BaseRefresh: tc.refresh})
			if len(got) != len(tc.want) {
				t.Fatalf("rows = %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("row %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
