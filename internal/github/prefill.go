package github

// The vocabulary the pull-request prefill (pullprefill.go, task 064 decision
// 9) matches a workflow's declared fields with.
//
// The issue half of the prefill — Candidate, Title, Description and LinkLine
// over a fetched GitHub issue, and the `issue`/`github_issue`/`labels`/
// `assignee`/`milestone` names it matched — was removed with the
// `github_issue` create field (task 130.11, task 130 decision 7). A task
// created from a GitHub issue is created from the vincent issue it was
// imported as, and internal/issues owns that prefill.

// The §8.1.2 field-type vocabulary, restated here so this package stays a
// leaf. internal/workflow owns the definition; these are the four spellings
// its `type:` accepts, and the mapping reads nothing else.
const (
	TypeString  = "string"
	TypeInteger = "integer"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
)

// FieldDecl is one workflow-declared field reduced to what the mapping reads.
// The caller keeps the full declaration and is the one that validates the
// candidate against it — this package deliberately owns no second copy of
// §8.1.2's validation (task 035 decision 7).
type FieldDecl struct {
	Name string
	Type string
}
