package trigger

import (
	"strings"
	"testing"
)

// TestParseVariantRefusals covers the rules 096.3–096.5 added, each at the
// path a form renders it against.
func TestParseVariantRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		action string
		edit   func(doc map[string]any)
		want   string
	}{
		{
			"github source refuses its own interval (decision 31D)", SourceGitHubIssues, ActionCreateTask,
			func(d map[string]any) { setPath(d, "source.poll_interval", "60s", false) }, "source.poll_interval",
		},
		{
			"untrusted issue event without allowed_actors (decision 31F)", SourceGitHubIssues, ActionCreateTask,
			func(d map[string]any) { setPath(d, "match.action", "opened", false) }, "allowed_actors",
		},
		{
			"no match.action matches every event, untrusted included", SourceGitHubPRs, ActionCreateTask,
			func(d map[string]any) { delete(d, "match") }, "allowed_actors",
		},
		{
			"review_requested is untrusted", SourceGitHubPRs, ActionCreateTask,
			func(d map[string]any) { setPath(d, "match.action", "review_requested", false) }, "allowed_actors",
		},
		{
			"a list with one untrusted member", SourceGitHubIssues, ActionCreateTask,
			func(d map[string]any) { setPath(d, "match.action", []any{"labeled", "reopened"}, false) }, "allowed_actors",
		},
		{
			"an event the source never synthesizes", SourceGitHubIssues, ActionCreateTask,
			func(d map[string]any) { setPath(d, "match.action", "merged", false) }, "match.action",
		},
		{
			"allowed_actors on a command source", SourceCommand, ActionCreateTask,
			func(d map[string]any) { d["allowed_actors"] = []any{"me"} }, "allowed_actors",
		},
		{
			"http without a signature", SourceHTTP, ActionCreateTask,
			func(d map[string]any) { setPath(d, "source.signature", nil, true) }, "source.signature",
		},
		{
			"http with a bad secret variable name", SourceHTTP, ActionCreateTask,
			func(d map[string]any) { setPath(d, "source.signature.secret_env", "not a name", false) }, "source.signature.secret_env",
		},
		{
			"http is never polled", SourceHTTP, ActionCreateTask,
			func(d map[string]any) { setPath(d, "source.poll_interval", "1m", false) }, "source.poll_interval",
		},
		{
			"cancel without on_fire (decision 31C)", SourceCommand, ActionCancel,
			func(d map[string]any) { delete(d, "on_fire") }, "on_fire",
		},
		{
			"cancel with on_fire propose", SourceCommand, ActionCancel,
			func(d map[string]any) { d["on_fire"] = OnFirePropose }, "on_fire",
		},
		{
			"follow_up needs a prompt", SourceCommand, ActionFollowUp,
			func(d map[string]any) { setPath(d, "action.prompt", nil, true) }, "action.prompt",
		},
		{
			"a reaction takes no title", SourceCommand, ActionRetry,
			func(d map[string]any) { setPath(d, "action.title", "x", false) }, "action.title",
		},
		{
			"a reaction takes no permission", SourceCommand, ActionRetry,
			func(d map[string]any) { d["permission"] = PermissionWorkflow }, "permission",
		},
		{
			"assigned on type: issues (task 130.15 decision 4)", SourceIssues, ActionCreateTask,
			func(d map[string]any) { setPath(d, "match.action", "assigned", false) }, "match.action",
		},
		{
			"an untrusted issues event sync can deliver needs allowed_actors", SourceIssues, ActionCreateTask,
			func(d map[string]any) { setPath(d, "match.action", "opened", false) }, "allowed_actors",
		},
		{
			"match.by naming sync keeps the allowlist", SourceIssues, ActionCreateTask,
			func(d map[string]any) {
				setPath(d, "match.action", "closed", false)
				setPath(d, "match.by", []any{"human", "sync"}, false)
			}, "allowed_actors",
		},
		{
			"lane_changed sync can deliver needs allowed_actors (task 134.7 decision 4)", SourceIssues, ActionCreateTask,
			func(d map[string]any) { setPath(d, "match.action", "lane_changed", false) }, "allowed_actors",
		},
		{
			"issues never polls", SourceIssues, ActionCreateTask,
			func(d map[string]any) { setPath(d, "source.poll_interval", "1m", false) }, "source.poll_interval",
		},
		{
			"issue with github_pull", SourceCommand, ActionCreateTask,
			func(d map[string]any) {
				setPath(d, "action.issue", "{{ .Event.n }}", false)
				setPath(d, "action.github_pull", "{{ .Event.n }}", false)
			}, "action.issue",
		},
		{
			"a reaction takes no issue", SourceCommand, ActionFollowUp,
			func(d map[string]any) { setPath(d, "action.issue", "{{ .Event.n }}", false) }, "action.issue",
		},
		{
			"issue is a template", SourceCommand, ActionCreateTask,
			func(d map[string]any) { setPath(d, "action.issue", "{{ .Event.n", false) }, "action.issue",
		},
		{
			"create_task takes no branch", SourceCommand, ActionCreateTask,
			func(d map[string]any) { setPath(d, "action.branch", "main", false) }, "action.branch",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := docFor(tc.source, tc.action)
			tc.edit(doc)
			if errs := parseDoc(t, doc); !hasPath(errs, tc.want) {
				t.Errorf("errors %v, want one at %s", errs, tc.want)
			}
		})
	}
}

// TestParseVariantAccepts: the guarded shapes that must load.
func TestParseVariantAccepts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		action string
		edit   func(doc map[string]any)
	}{
		{"trusted label event alone", SourceGitHubIssues, ActionCreateTask, func(map[string]any) {}},
		{
			"untrusted event with an allowlist", SourceGitHubIssues, ActionCreateTask,
			func(d map[string]any) {
				setPath(d, "match.action", "opened", false)
				d["allowed_actors"] = []any{"lezli01"}
			},
		},
		{"merge cancels, unattended", SourceGitHubPRs, ActionCancel, func(map[string]any) {}},
		{"trusted issues label event alone", SourceIssues, ActionCreateTask, func(map[string]any) {}},
		{
			"a local-only opened needs no allowlist (task 130.15 decision 1)", SourceIssues, ActionCreateTask,
			func(d map[string]any) {
				setPath(d, "match.action", "opened", false)
				setPath(d, "match.by", "human", false)
				setPath(d, "action.issue", "{{ .Event.issue_id }}", false)
			},
		},
		{
			"an issues opened with an allowlist", SourceIssues, ActionCreateTask,
			func(d map[string]any) {
				setPath(d, "match.action", "opened", false)
				d["allowed_actors"] = []any{"lezli01"}
			},
		},
		{
			"a local-only lane_changed needs no allowlist", SourceIssues, ActionCreateTask,
			func(d map[string]any) {
				setPath(d, "match.action", "lane_changed", false)
				setPath(d, "match.by", []any{"human", "task"}, false)
			},
		},
		{
			"an action-less issues trigger with an allowlist still loads", SourceIssues, ActionCreateTask,
			func(d map[string]any) {
				delete(d, "match")
				d["allowed_actors"] = []any{"lezli01"}
			},
		},
		{"signed push retries", SourceHTTP, ActionRetry, func(map[string]any) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := docFor(tc.source, tc.action)
			tc.edit(doc)
			if errs := parseDoc(t, doc); len(errs) > 0 {
				t.Errorf("refused: %v", errs)
			}
		})
	}
}

// TestDeprecations: github_issues earns a warning and still loads (task
// 130.15 decision 6); no other source does.
func TestDeprecations(t *testing.T) {
	for _, src := range SourceTypes() {
		doc := docFor(src, ActionCreateTask)
		if errs := parseDoc(t, doc); len(errs) > 0 {
			t.Fatalf("%s: refused: %v", src, errs)
		}
		// An allowlist keeps the issues source clear of its Jira label
		// note, which TestJiraLabelNote pins on its own.
		d := &Definition{Source: Source{Type: src}, AllowedActors: []string{"alice"}}
		got := Deprecations(d)
		if (src == SourceGitHubIssues) != (len(got) == 1) {
			t.Errorf("%s: deprecations = %v", src, got)
		}
	}
}

// TestSyncTrustIsTrackerNeutral pins task 142.17 decision 2: the
// allowed_actors refusal for a by: sync change on an issues source names the
// issue's tracker, GitHub or Jira, rather than GitHub alone — and it is still
// a refusal for every action sync can deliver, so the rule itself is
// unchanged.
func TestSyncTrustIsTrackerNeutral(t *testing.T) {
	for _, action := range []string{"opened", "closed", "reopened", "lane_changed"} {
		t.Run(action, func(t *testing.T) {
			doc := docFor(SourceIssues, ActionCreateTask)
			setPath(doc, "match.action", action, false)
			var msg string
			for _, e := range parseDoc(t, doc) {
				if e.Path == "allowed_actors" {
					msg = e.Message
				}
			}
			if msg == "" {
				t.Fatalf("by: sync on %s loaded without allowed_actors", action)
			}
			for _, want := range []string{"the issue's tracker", "GitHub or Jira", "leave sync out of match.by"} {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q lacks %q", msg, want)
				}
			}
			if strings.Contains(msg, "public repository") {
				t.Errorf("message %q still gives the GitHub-only rationale", msg)
			}
		})
	}
}
