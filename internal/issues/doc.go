// Package issues is the one validated write path for vincent's issues
// (spec §5.6, task 130). Every writer — the REST API, GitHub sync and event
// triggers — goes through a Service, so the input rules (title and label
// length, the shape of a kind, the priority range, a close reason) and the
// actor each change is attributed to are decided here, once, rather than by
// each caller.
//
// The store deliberately does not check those rules. internal/store enforces
// only what the schema and internal/issuestate need — that the project
// exists and that a transition is legal for its actor — and it does that
// inside the transaction, so this package never re-implements the state
// machine: it validates the request, then lets the store consult issuestate.
// Store errors (store.ErrNotFound, store.ErrIssueChanged,
// store.ErrInvalidIssueAction) pass through untouched, so errors.Is works
// for every caller; a refused input is a *ValidationError naming its field,
// which the API maps to a 400.
//
// It is a package of its own rather than more methods on store.Store for
// the reason the store has none of these rules: the store is the one DB
// writer and stays a typed persistence layer, while what a human, an agent
// or sync may write is product policy that the API, sync and triggers must
// share. Folding it into the store would bind every store caller — recovery,
// imports, test fixtures — to that policy, and would leave the policy beside
// SQL nobody outside the store reads. This package imports only
// internal/store and internal/issuestate; it never imports internal/api.
package issues
