package trigger

import "testing"

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
