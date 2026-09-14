package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

const baseSHAFixture = "abc1234def5678abc1234def5678abc1234def56"

// baseOverview renders the task detail document for a fixture carrying the
// given base, both raw (for the styling) and stripped (for the text).
func baseOverview(t *testing.T, sha string, refresh *apiclient.BaseRefresh) (raw, plain string) {
	t.Helper()
	d := taskDetailFixture(t)
	d.task.BaseBranch = "master"
	d.task.BaseSHA = sha
	d.task.BaseRefresh = refresh
	raw = strings.Join(newTaskView(d).detailLines(120), "\n")
	return raw, ansi.Strip(raw)
}

func plainLineWith(plain, label string) string {
	for _, line := range strings.Split(plain, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label+" ") {
			return line
		}
	}
	return ""
}

func TestTaskDetailOverviewBaseRow(t *testing.T) {
	_, plain := baseOverview(t, baseSHAFixture, nil)
	if line := plainLineWith(plain, "base"); !strings.Contains(line, "master @ abc1234") {
		t.Errorf("base row = %q, want master @ abc1234:\n%s", line, plain)
	}
	if strings.Contains(plain, "base refresh") {
		t.Errorf("a task with no refresh record rendered a refresh row:\n%s", plain)
	}

	_, plain = baseOverview(t, "", nil)
	line := plainLineWith(plain, "base")
	if !strings.HasSuffix(strings.TrimSpace(line), "master") || strings.Contains(line, "@") {
		t.Errorf("base row without base_sha = %q, want the branch alone:\n%s", line, plain)
	}
}

func TestTaskDetailOverviewBaseRefreshWarning(t *testing.T) {
	fetched := apiclient.BaseFetch{Result: "fetched", Remote: "origin", Ref: "refs/heads/master"}
	for _, tc := range []struct {
		name    string
		refresh *apiclient.BaseRefresh
		want    string
	}{
		{
			name: "fetch failed",
			refresh: &apiclient.BaseRefresh{
				Fetch: apiclient.BaseFetch{
					Result: "error", Remote: "origin", Ref: "refs/heads/master", Error: "could not resolve host",
				},
				FastForward: apiclient.BaseFastForward{Result: "not_attempted"},
			},
			want: "fetch from origin refs/heads/master failed: could not resolve host; base may be stale",
		},
		{
			name: "fast-forward skipped",
			refresh: &apiclient.BaseRefresh{
				Fetch:       fetched,
				FastForward: apiclient.BaseFastForward{Result: "skipped", Reason: "checkout_dirty"},
			},
			want: "local master not fast-forwarded: checkout_dirty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, plain := baseOverview(t, baseSHAFixture, tc.refresh)
			if line := plainLineWith(plain, "base refresh"); !strings.Contains(line, tc.want) {
				t.Errorf("base refresh row = %q, want %q:\n%s", line, tc.want, plain)
			}
			if !strings.Contains(raw, styleWarn.Render(tc.want)) {
				t.Errorf("the base refresh row is not styled as a warning:\n%q", raw)
			}
		})
	}

	for _, result := range []string{"advanced", "up_to_date"} {
		_, plain := baseOverview(t, baseSHAFixture, &apiclient.BaseRefresh{
			Fetch: fetched, FastForward: apiclient.BaseFastForward{Result: result},
		})
		if strings.Contains(plain, "base refresh") {
			t.Errorf("%s rendered a base refresh warning:\n%s", result, plain)
		}
	}
}

// The unlinked pull request's `base` is the task's starting point; a linked
// one's stays the pull request's own base branch.
func TestTaskPullUnlinkedBaseShowsSHA(t *testing.T) {
	v := taskPullFixture(t, apiclient.GitHubTaskPull{CompareURL: compareURLFixture})
	v.detail.task.BaseBranch = "master"
	v.detail.task.BaseSHA = baseSHAFixture
	plain := ansi.Strip(strings.Join(v.pullSectionLines(100), "\n"))
	// The section lays its facts out in two columns, so `base` shares a line.
	if !strings.Contains(plain, "base               master @ abc1234") {
		t.Errorf("unlinked pull base fact is not master @ abc1234:\n%s", plain)
	}
}
