package issuesource

import (
	"context"

	"github.com/lezli01/vincent/internal/store"
)

// The optional capabilities a Jira source brings to the importer (task
// 142.12, issue #908). Like RepoCommentLister and PlaceholderAdopter they
// are found by type assertion; a source without one is served by the
// importer's general path, so GitHub's behaviour is untouched.

// ReasonBindingChanged is a sync row whose binding now resolves to a
// different provider project than the one its issues were imported from
// (task 142 decision 13.6): the project key was renamed onto, or reassigned
// to, another project. The pass stops and makes no further call; a human
// rebinds, which deletes the row and with it the recorded id.
const ReasonBindingChanged = "binding_changed"

// BulkGetMax is the most remotes one BulkGetter.GetIssues call takes —
// Jira's bulkfetch limit.
const BulkGetMax = 100

// BulkIssue is one issue a BulkGetter read.
type BulkIssue struct {
	// Issue is the issue as mapped. When MovedTo is set it carries the
	// issue's NEW Repo, Number and RemoteKey.
	Issue Issue
	// MovedTo is the issue's new browse URL when it left the binding's
	// project; "" when it is still at the binding.
	MovedTo string
}

// BulkGetter is a source that reads many issues in one call (Jira's
// bulkfetch). The sweep probes the open remotes its listing lacked through
// it, BulkGetMax at a time, instead of one GetIssue each.
type BulkGetter interface {
	// GetIssues reads up to BulkGetMax remotes at b, keyed by RemoteKey. A
	// remote with no entry was absent — deleted or unreadable, which the
	// provider does not distinguish — and the importer marks it missing,
	// never deletes it.
	GetIssues(ctx context.Context, b Binding, rems []store.IssueRemote) (map[string]BulkIssue, error)
}

// BindingResolver is a source whose binding resolves, over the network, to
// the provider's stable project id (Jira: GET project/{key}). The importer
// calls it at the start of every pass, records the id on the sync row the
// first time, and stops with ReasonBindingChanged when it later differs.
type BindingResolver interface {
	// ResolveBinding returns b resolved — resolved.String() == b.String() —
	// and the provider's id of b's project. Its error goes through the
	// source's Classify (a 404 is ReasonNotFound). The importer uses
	// resolved for the rest of the pass.
	ResolveBinding(ctx context.Context, b Binding) (resolved Binding, remoteProjectID string, err error)
}
