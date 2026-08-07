package store

import (
	"encoding/json"
	"time"
)

// TaskState enumerates task lifecycle states (spec §6).
type TaskState string

// Task lifecycle states (spec §6).
const (
	TaskQueued       TaskState = "queued"
	TaskRunning      TaskState = "running"
	TaskAwaitingGate TaskState = "awaiting_gate"
	TaskBlocked      TaskState = "blocked"
	TaskPaused       TaskState = "paused"
	TaskDone         TaskState = "done"
	TaskAborted      TaskState = "aborted"
	TaskArchived     TaskState = "archived"
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
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
	State            TaskState
	CurrentStep      int
	BlockReason      string // set while State == TaskBlocked
	CreatedAt        time.Time
	UpdatedAt        time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
	ArchivedAt       *time.Time
}

// StepRun is one attempt at executing one step of one task; history is
// append-only (spec §5.4).
type StepRun struct {
	ID             int64
	TaskID         int64
	StepIndex      int
	StepID         string
	StepType       string // agent | command | manual
	Attempt        int    // 1-based
	State          StepRunState
	Agent          string // adapter name; agent steps only
	PID            *int   // while running
	ProcStartedAt  *time.Time
	ExitCode       *int
	CheckExitCode  *int
	FailureReason  string
	ResultSummary  string
	TranscriptPath string
	InputTokens    *int64
	OutputTokens   *int64
	CostUSD        *float64 // nil when the agent doesn't report cost
	StartedAt      time.Time
	FinishedAt     *time.Time
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
