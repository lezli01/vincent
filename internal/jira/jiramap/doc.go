// Package jiramap defines, once, how a Jira Cloud issue's workflow state maps
// onto vincent's two-state issue model of spec §5.6 (internal/issuestate),
// and how a vincent close or reopen picks the Jira transition that carries it
// back. It is pure: no I/O, no clock, and nothing but the standard library
// and internal/issuestate. The Jira importer and the Jira outbox writer both
// consult it, so "what does this Jira status mean" has one answer — the same
// argument internal/issuestate makes for §5.6's own table.
//
// It is the first in-tree Jira code, and rests on the departures recorded by
// the Jira issue-sync task document (#897, part of #896) from task 096
// decision 2, task 130 decisions 15.1 and 15.4, and task 137 decision 10. It
// parallels task 130's GitHub mapping: decision 3 (the reason vocabulary
// stays GitHub's — Jira gets a mapping onto it, never new reasons), decision
// 15.4 (kind is seeded on first import only; priority stays local) and
// decision 17.4 (a duplicate's target is resolved by the caller against
// same-project imports).
//
// # Reading Jira (FromJira)
//
// Jira statuses are per workflow; only statusCategory.key is fixed. An issue
// is closed if and only if its status category is "done" — "new",
// "indeterminate", "undefined" and any unknown key are open. Resolution is
// never consulted for openness: team-managed projects have none, and
// reopening does not reliably clear it. Config cannot declare a done status
// open; it covers transition choice and reason sets, never openness.
//
// The close reason comes from the resolution: a name in the duplicate set is
// `duplicate`, one in the not-planned set is `not_planned`, and anything else
// is `completed`, which is issuestate.ResolveReason's own default. Names are
// compared trimmed and case-folded.
//
// One narrowing applies on the reason side only: when the resolution is
// null, a done status whose name is in the not-planned set, or is
// "Cancelled"/"Canceled", reads as `not_planned`. A team-managed "Won't do →
// Cancelled" transition sets no resolution, and without this fallback the
// chooser's achieved reason and the next read-back would disagree on every
// poll (the echo trap). A set resolution always wins over the status name.
//
// # Writing Jira (ChooseTransition)
//
// The chooser considers only the transitions passed in — those Jira offers
// now — and never plans a multi-hop path. It returns exactly one transition,
// with the resolution to set (only ever when the transition's screen offers
// the field, and never on reopen), or a typed ChooseError whose Code is
// `no_transition`, `ambiguous_transition` or `transition_needs_fields`. It
// never degrades a requested reason to another one: every success reports an
// Achieved outcome equal to the request, and Achieved is what FromJira will
// read back after the transition.
//
// Where both a name-matching transition (say "Won't do") and a generic done
// transition whose resolution list happens to offer the reason (say "Done")
// can achieve `not_planned` or `duplicate`, the name-matching one is
// preferred rather than reported ambiguous; for `duplicate`, a done
// transition not named for a not-planned close is preferred next. A Config
// override settles any other choice.
package jiramap
