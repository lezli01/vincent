package apiclient

import (
	"context"

	"github.com/lezli01/vincent/internal/doctor"
)

// The doctor wire types below are **aliases**, not copies (task 005
// decision 6).
//
// This package normally declares its own shape for each response, because the
// server's DTOs are unexported and duplicating them is what keeps the daemon
// free to change an internal struct. The doctor report is the exception, and
// deliberately so: internal/doctor is a shared package that *both* sides
// import, because a report has to be composable client-side when no daemon
// answers. A second declaration here would be a second definition of the same
// document, and the two would drift — which is the one failure this design
// exists to prevent. An alias is the same type, so drift cannot happen and the
// live test still proves the JSON round-trips.

// DoctorReport is the whole GET /v1/doctor body.
type DoctorReport = doctor.Report

// DoctorPaths is the report's §12.2 directories group.
type DoctorPaths = doctor.Paths

// DoctorDaemon is the report's liveness group.
type DoctorDaemon = doctor.Daemon

// DoctorLog is the report's daemon-log group.
type DoctorLog = doctor.Log

// DoctorDatabase is the report's §14 store group.
type DoctorDatabase = doctor.Database

// DoctorAgent is one adapter's §9.5 availability in the report.
type DoctorAgent = doctor.Agent

// DoctorStorage is the report's disk and worktree group.
type DoctorStorage = doctor.Storage

// DoctorOrphan is a worktree directory no live task owns.
type DoctorOrphan = doctor.Orphan

// DoctorTasks is the report's §6 state tally.
type DoctorTasks = doctor.Tasks

// DoctorProblem is one finding from the closed unhealthy set.
type DoctorProblem = doctor.Problem

// DoctorFixAction is one repair POST /v1/doctor/fix attempted.
type DoctorFixAction = doctor.FixAction

// DoctorFixResult is the POST /v1/doctor/fix body.
type DoctorFixResult = doctor.FixResult

// Daemon liveness values a report's Daemon.Status carries (§12.1).
const (
	DaemonRunning      = doctor.StatusRunning
	DaemonNotRunning   = doctor.StatusNotRunning
	DaemonUnresponsive = doctor.StatusUnresponsive
)

// Repair actions and outcomes from POST /v1/doctor/fix.
const (
	FixActionRemoveWorktree  = doctor.ActionRemoveWorktree
	FixActionCompactDatabase = doctor.ActionCompactDatabase

	FixDone    = doctor.FixDone
	FixSkipped = doctor.FixSkipped
	FixFailed  = doctor.FixFailed
)

// Doctor fetches the daemon's diagnostic report (§17, §13.2).
//
// The daemon always re-probes agent authentication for this endpoint (§9.6):
// auth state is not a pure function of the binary, so a cached answer would
// tell a user who has just logged in that they are still logged out — the
// exact loop doctor exists for.
func (c *Client) Doctor(ctx context.Context) (*DoctorReport, error) {
	var rep DoctorReport
	if err := c.get(ctx, "/v1/doctor", &rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

// DoctorFix asks the daemon to remove orphaned worktrees and compact the
// database, and returns what it did plus a report taken afterwards. force
// additionally removes orphans with local changes.
//
// The repair is a POST, never a side effect of the GET (decision 5): a
// read-only report and a call that deletes directories are two different
// promises, so they are two different requests.
func (c *Client) DoctorFix(ctx context.Context, force bool) (*DoctorFixResult, error) {
	var res DoctorFixResult
	if err := c.post(ctx, "/v1/doctor/fix", map[string]bool{"force": force}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
