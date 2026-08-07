// Package workflow implements the YAML workflow registry — strict decoding,
// validation with file/line errors, the built-in, global and project scopes
// with shadowing, and live reload — plus the step template engine (spec §8;
// T2.1–T2.2).
//
// The registry is the daemon's source of workflow definitions; a task
// captures the entry's Source as its workflow_snapshot at creation, so later
// edits never mutate an in-flight or historical task (§5.3).
package workflow
