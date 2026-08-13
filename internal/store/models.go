package store

import (
	"encoding/json"
	"time"

	"github.com/lezli01/vincent/internal/taskstate"
)

// TaskState is the task lifecycle state (spec §6). It is an alias, not a
// second type: this list and taskstate's had already drifted apart once —
// `awaiting_input` existed in the state machine and not here, which would
// have silently under-counted the §11 caps — and a third copy must not
// become possible.
type TaskState = taskstate.State

// Task lifecycle states (spec §6), re-exported so store callers need not
// import taskstate for a literal.
const (
	TaskQueued        = taskstate.Queued
	TaskRunning       = taskstate.Running
	TaskAwaitingGate  = taskstate.AwaitingGate
	TaskAwaitingInput = taskstate.AwaitingInput
	TaskBlocked       = taskstate.Blocked
	TaskPaused        = taskstate.Paused
	TaskDone          = taskstate.Done
	TaskAborted       = taskstate.Aborted
	TaskArchived      = taskstate.Archived
)

// StepRunState enumerates step-run attempt states (spec §5.4).
type StepRunState string

// Step-run attempt states (spec §5.4).
const (
	StepRunning     StepRunState = "running"
	StepSucceeded   StepRunState = "succeeded"
	StepFailed      StepRunState = "failed"
	StepInterrupted StepRunState = "interrupted"
	StepApproved    StepRunState = "approved"
	StepRejected    StepRunState = "rejected"
	StepSkipped     StepRunState = "skipped"
)

// Project is a registered local git repository (spec §5.1).
type Project struct {
	ID               int64
	Name             string
	Path             string
	DefaultBranch    string
	DefaultWorkflow  string // "" = none
	MaxParallelTasks *int   // nil = unlimited (global cap still applies)
	// BranchTemplate names this project's branch convention; "" inherits
	// config.yaml's, and an unset config falls back to the built-in
	// vincent/{id}-{slug} (task 001).
	BranchTemplate string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Task is one unit of work delivered by running a workflow against a project
// (spec §5.3).
type Task struct {
	ID               int64
	ProjectID        int64
	Title            string
	Description      string
	Fields           map[string]string
	WorkflowName     string
	WorkflowSnapshot string
	BaseBranch       string
	BranchName       string
	WorktreePath     string // "" until the worktree is created
	Priority         int
	AgentOverride    string // task-level selection (spec §8.6); "" = none
	ModelOverride    string
	EffortOverride   string
	State            TaskState
	CurrentStep      int
	BlockReason      string // set while State == TaskBlocked
	// PauseRequested is a pause accepted while running but not yet taken
	// effect (spec §6). Persisted so a crash, which re-queues the task,
	// cannot discard it.
	PauseRequested bool
	// RetryCursorAt is the last human retry. The retry budget counts only
	// failed attempts after it, which is how retry resets it (§6, §7.2).
	RetryCursorAt *time.Time
	// PendingOverride is edit+retry text waiting for the next attempt's
	// step run; the actor drains and clears it (§6).
	PendingOverride *Override
	// PendingInputJSON is the normalized InputRequest while the task is
	// awaiting_input (§7.4); "" otherwise. TransitionTask clears it on any
	// transition out of awaiting_input.
	PendingInputJSON string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
	ArchivedAt       *time.Time
}

// StepRun is one attempt at executing one step of one task; history is
// append-only (spec §5.4).
type StepRun struct {
	ID            int64
	TaskID        int64
	StepIndex     int
	StepID        string
	StepType      string // agent | command | manual
	Attempt       int    // 1-based
	State         StepRunState
	Agent         string // adapter name; agent steps only
	Model         string // resolved model as passed to the adapter (spec §8.6); "" = CLI default
	Effort        string // resolved effort as passed to the adapter; "" = CLI default
	PID           *int   // while running
	ProcStartedAt *time.Time
	ExitCode      *int
	CheckExitCode *int
	FailureReason string
	ResultSummary string
	// PromptOverride and RunOverride record the text a human supplied for
	// this attempt via edit+retry (spec §6). Empty on every other attempt,
	// including later automatic retries of the same step: the edit happened
	// at a point in time, even though the snapshot keeps it thereafter.
	PromptOverride string
	RunOverride    string
	TranscriptPath string
	InputTokens    *int64
	OutputTokens   *int64
	CostUSD        *float64 // nil when the agent doesn't report cost
	// InputWaitMS is the time this attempt spent in awaiting_input (§7.4),
	// excluded from duration metrics (§17).
	InputWaitMS int64
	StartedAt   time.Time
	FinishedAt  *time.Time
}

// Override is the prompt or command a human supplied with `edit + retry`
// (spec §6). Exactly one field is set: a prompt for an agent step, a run for
// a command step.
type Override struct {
	Prompt string `json:"prompt,omitempty"`
	Run    string `json:"run,omitempty"`
}

// Empty reports whether the override carries nothing.
func (o Override) Empty() bool { return o.Prompt == "" && o.Run == "" }

// Candidate is one queued task considered for admission, carrying the cap
// context the scheduler needs to decide (spec §11). The slot counts are as
// of the query; the scheduler adds its own in-flight admissions on top,
// which SQL cannot see.
type Candidate struct {
	Task Task
	// ProjectSlots is how many of the project's tasks already hold a slot.
	ProjectSlots int
	// ProjectCap is the project's max_parallel_tasks; nil = unlimited.
	ProjectCap *int
}

// Event is a durable state event (spec §13.3). ID doubles as the SSE
// Last-Event-ID cursor.
type Event struct {
	ID        int64
	TS        time.Time
	Type      string
	TaskID    *int64
	ProjectID *int64
	Payload   json.RawMessage
}
