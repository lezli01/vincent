package github

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// threadCorpus holds issue 1 with n comments created a minute apart — and
// updated in reverse, so an answer in update order would read backwards —
// issue 2 with none, and pull request 3 with one conversation comment.
func threadCorpus(t *testing.T, n int) string {
	t.Helper()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	issue := func(num int, extra map[string]any) map[string]any {
		row := map[string]any{
			"id": 9000 + num, "node_id": fmt.Sprintf("I_%d", num), "number": num,
			"title": fmt.Sprintf("issue %d", num), "state": "open",
			"created_at": base.Format(time.RFC3339), "updated_at": base.Format(time.RFC3339),
			"url":            fmt.Sprintf("https://api.github.com/repos/octo/repo/issues/%d", num),
			"repository_url": "https://api.github.com/repos/octo/repo",
			"html_url":       fmt.Sprintf("https://github.com/octo/repo/issues/%d", num),
			// Wrong on purpose: the fake counts the comment rows instead.
			"comments": 7,
		}
		for k, v := range extra {
			row[k] = v
		}
		return row
	}
	comment := func(id, num int, created, updated time.Time) map[string]any {
		return map[string]any{
			"id": id, "node_id": fmt.Sprintf("IC_%d", id),
			"issue_url": fmt.Sprintf("https://api.github.com/repos/octo/repo/issues/%d", num),
			"html_url":  fmt.Sprintf("https://github.com/octo/repo/issues/%d#issuecomment-%d", num, id),
			"body":      fmt.Sprintf("comment %d", id), "user": map[string]any{"login": "hubot"},
			"created_at": created.Format(time.RFC3339), "updated_at": updated.Format(time.RFC3339),
		}
	}
	rows := []map[string]any{
		issue(1, nil), issue(2, nil),
		issue(3, map[string]any{"pull_request": map[string]any{"url": "https://api.github.com/repos/octo/repo/pulls/3"}}),
		comment(50, 3, base.Add(time.Hour), base.Add(time.Hour)),
	}
	for i := range n {
		created := base.Add(time.Duration(i+1) * time.Minute)
		updated := base.Add(time.Duration(10*n-i) * time.Minute)
		rows = append(rows, comment(100+i, 1, created, updated))
	}
	return writeCorpus(t, rows)
}

// TestIssueCommentCount: the sync's two reads carry GitHub's count, on both
// legs, and the picker's carry none on either (decision 1).
func TestIssueCommentCount(t *testing.T) {
	for via, c := range legs(t, threadCorpus(t, 3), "success") {
		page, err := c.ListIssuesSince(t.Context(), octoRepo, ListSinceOptions{})
		if err != nil {
			t.Fatalf("%s: %v", via, err)
		}
		got := map[int]int{}
		for _, issue := range page.Issues {
			got[issue.Number] = issue.Comments
		}
		if want := map[int]int{1: 3, 2: 0}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: ListIssuesSince counts = %v, want %v", via, got, want)
		}
		one, err := c.GetIssue(t.Context(), octoRepo, 1)
		if err != nil || one.Comments != 3 {
			t.Errorf("%s: GetIssue = %d, %v; want 3", via, one.Comments, err)
		}
		picked, err := c.List(t.Context(), octoRepo, ListOptions{State: StateAll})
		if err != nil {
			t.Fatalf("%s: List: %v", via, err)
		}
		for _, issue := range picked {
			if issue.Comments != 0 {
				t.Errorf("%s: List #%d carries a count (%d) the other leg cannot", via, issue.Number, issue.Comments)
			}
		}
	}
}

func TestParseRESTCommentCount(t *testing.T) {
	body := []byte(`{"number":4,"title":"t","state":"open","comments":12,` +
		`"url":"https://api.github.com/repos/octo/repo/issues/4","repository_url":"https://api.github.com/repos/octo/repo"}`)
	issue, err := parseAPIIssue(body, octoRepo, 4, fixtureNow)
	if err != nil || issue.Comments != 12 {
		t.Errorf("parseAPIIssue = %d, %v; want 12", issue.Comments, err)
	}
}

// TestListCommentsOfIssuePagesToTheEnd: 150 comments are two pages at 100,
// and the whole thread comes back oldest first, every comment filed under
// the number asked about and none of another issue's.
func TestListCommentsOfIssuePagesToTheEnd(t *testing.T) {
	var answers [][]IssueComment
	for via, c := range legs(t, threadCorpus(t, 150), "success") {
		thread, err := c.ListCommentsOfIssue(t.Context(), octoRepo, 1)
		if err != nil {
			t.Fatalf("%s: %v", via, err)
		}
		if len(thread) != 150 {
			t.Fatalf("%s: %d comments, want 150", via, len(thread))
		}
		for i, cm := range thread {
			if cm.ID != int64(100+i) || cm.IssueNumber != 1 || cm.Author != "hubot" ||
				cm.Body != fmt.Sprintf("comment %d", 100+i) || cm.NodeID == "" || cm.URL == "" {
				t.Fatalf("%s: comment %d = %+v", via, i, cm)
			}
		}
		answers = append(answers, thread)

		empty, err := c.ListCommentsOfIssue(t.Context(), octoRepo, 2)
		if err != nil || len(empty) != 0 {
			t.Errorf("%s: empty thread = %v, %v", via, empty, err)
		}
		pull, err := c.ListCommentsOfIssue(t.Context(), octoRepo, 3)
		if err != nil || len(pull) != 1 || pull[0].ID != 50 || pull[0].IssueNumber != 3 {
			t.Errorf("%s: pull request thread = %+v, %v", via, pull, err)
		}
	}
	if !reflect.DeepEqual(answers[0], answers[1]) {
		t.Error("the two legs read the thread differently")
	}
}

func TestListCommentsOfIssueErrors(t *testing.T) {
	for via, c := range legs(t, threadCorpus(t, 1), "success") {
		_, err := c.ListCommentsOfIssue(t.Context(), octoRepo, 99)
		if ReasonOf(err) != ReasonNotFound {
			t.Errorf("%s: absent issue = %v, want not_found", via, err)
		}
		if _, err := c.ListCommentsOfIssue(t.Context(), octoRepo, 0); ReasonOf(err) != ReasonBadRequest {
			t.Errorf("%s: number 0 = %v, want bad_request", via, err)
		}
	}
	for via, c := range legs(t, threadCorpus(t, 1), "rate-limited") {
		_, err := c.ListCommentsOfIssue(t.Context(), octoRepo, 1)
		var e *Error
		if !errors.As(err, &e) || e.Reason != ReasonRateLimited || e.ResetAt.IsZero() {
			t.Errorf("%s: %v, want rate_limited with a reset", via, err)
		}
	}
}
