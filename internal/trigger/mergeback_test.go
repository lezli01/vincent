package trigger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestCreateBodyWithoutMergeBackIsUnchanged: the task 040 idempotency digest
// is over the replayed bytes, so a body that does not ask for merge_back must
// marshal exactly as it did before the field existed (task 134.15).
func TestCreateBodyWithoutMergeBackIsUnchanged(t *testing.T) {
	issue, pull, cost := int64(3), 4, 2.5
	for _, tc := range []struct {
		body CreateBody
		want string
	}{
		{CreateBody{ProjectID: 1, Title: "t"}, `{"project_id":1,"title":"t"}`},
		{
			CreateBody{
				ProjectID: 7, Workflow: "w", Title: "t", Description: "d", Fields: map[string]string{"k": "v"},
				IssueID: &issue, GitHubPull: &pull, Paused: true, Restricted: true, MaxTaskCostUSD: &cost,
			},
			`{"project_id":7,"workflow":"w","title":"t","description":"d","fields":{"k":"v"},"issue_id":3,"github_pull":4,"paused":true,"restricted":true,"max_task_cost_usd":2.5}`,
		},
	} {
		b, err := json.Marshal(tc.body)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != tc.want {
			t.Errorf("body = %s\nwant   %s", b, tc.want)
		}
	}
}

// TestFiredActionReplaysMergeBack: a create_task action's merge_back reaches
// the POST /v1/tasks body, with an empty on_conflict sent as block, and an
// action without one sends no merge_back key at all.
func TestFiredActionReplaysMergeBack(t *testing.T) {
	events := fixtureEvents(t, "jira-search-v3.ndjson")
	for _, tc := range []struct {
		name  string
		extra string
		want  string
	}{
		{"agent", "  issue: '42'\n  merge_back:\n    on_conflict: agent\n", `"merge_back":{"on_conflict":"agent"}`},
		{"block", "  issue: '42'\n  merge_back:\n    on_conflict: block\n", `"merge_back":{"on_conflict":"block"}`},
		{"empty means block", "  issue: '42'\n  merge_back: {}\n", `"merge_back":{"on_conflict":"block"}`},
		{"absent", "  issue: '42'\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openStore(t)
			api := newFakeAPI(t, st, 0, "")
			if _, err := fire(t.Context(), st, api, jiraDef(t, tc.extra), events[0], time.Now()); err != nil {
				t.Fatal(err)
			}
			reqs := api.requests()
			if len(reqs) != 1 || reqs[0].path != createPath {
				t.Fatalf("replays = %+v, want one POST %s", reqs, createPath)
			}
			raw := string(reqs[0].raw)
			if !strings.Contains(raw, `"issue_id":42`) {
				t.Errorf("body %s carries no issue_id 42", raw)
			}
			if tc.want == "" {
				if strings.Contains(raw, "merge_back") {
					t.Errorf("body %s carries merge_back, want none", raw)
				}
				return
			}
			if !strings.Contains(raw, tc.want) {
				t.Errorf("body %s, want it to carry %s", raw, tc.want)
			}
		})
	}
}

// TestMergeBackValidation: on_conflict takes block, agent or nothing; the
// key needs action.issue, and only a create_task takes it at all.
func TestMergeBackValidation(t *testing.T) {
	withIssue := func(action string, mb any) map[string]any {
		doc := docFor(SourceCommand, action)
		if action == ActionCreateTask {
			setPath(doc, "action.issue", "{{ .Event.issue }}", false)
		}
		setPath(doc, "action.merge_back", mb, false)
		return doc
	}
	for _, v := range []string{MergeBackBlock, MergeBackAgent, ""} {
		mb := map[string]any{}
		if v != "" {
			mb["on_conflict"] = v
		}
		if errs := parseDoc(t, withIssue(ActionCreateTask, mb)); len(errs) > 0 {
			t.Errorf("on_conflict %q refused: %v", v, errs)
		}
	}
	if errs := parseDoc(t, withIssue(ActionCreateTask, map[string]any{"on_conflict": "merge"})); !hasPath(errs, "action.merge_back.on_conflict") {
		t.Errorf("on_conflict merge: errors %v, want one at action.merge_back.on_conflict", errs)
	}

	doc := validDoc()
	setPath(doc, "action.merge_back", map[string]any{"on_conflict": MergeBackAgent}, false)
	if errs := parseDoc(t, doc); !hasPath(errs, "action.merge_back") {
		t.Errorf("merge_back without issue: errors %v, want one at action.merge_back", errs)
	}

	for _, action := range []string{ActionFollowUp, ActionRetry, ActionCancel} {
		if errs := parseDoc(t, withIssue(action, map[string]any{"on_conflict": MergeBackBlock})); !hasPath(errs, "action.merge_back") {
			t.Errorf("merge_back on %s: errors %v, want one at action.merge_back", action, errs)
		}
	}
}
