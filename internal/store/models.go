package store

import (
	"encoding/json"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/github"
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
	// TaskAwaitingChildren is a fan-out parent waiting on its lanes (§7.6,
	// task 014). It holds no slot.
	TaskAwaitingChildren = taskstate.AwaitingChildren
	TaskBlocked          = taskstate.Blocked
	TaskPaused           = taskstate.Paused
	TaskDone             = taskstate.Done
	TaskAborted          = taskstate.Aborted
	TaskArchived         = taskstate.Archived
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
	// StepStopped is a `condition` step whose guard was false (§7.7, task
	// 015): the sequence ends here and the task is `done`. It is the one
	// state that is neither a success nor a failure of the step — the step
	// evaluated perfectly, and its answer was "stop".
	StepStopped StepRunState = "stopped"
)

// SkipReasonCondition is written to StepRun.SkipReason when a false `if:`
// guard skipped the step (§7.7, task 015). A human `skip` (§6) leaves it
// empty, which is what tells the two apart.
const SkipReasonCondition = "condition"

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
	// BaseSHA is the commit BranchName was actually cut from, recorded when
	// the worktree path fetched BaseBranch from its upstream (§5.3, §10 —
	// task 056). "" means BaseBranch itself still names the fork point, which
	// is every task created before the fetch existed or with
	// `fetch_base_branch: false`.
	BaseSHA        string
	Priority       int
	AgentOverride  string // task-level selection (spec §8.6); "" = none
	ModelOverride  string
	EffortOverride string
	State          TaskState
	CurrentStep    int
	BlockReason    string // set while State == TaskBlocked
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
	// PendingRepair is an ad-hoc repair the human asked for while the task
	// was blocked (§6, task 025). The actor runs it on the admission it
	// produced and drains it with the transition that returns the task to
	// `blocked`; leaving `blocked` any other way drops it, so a request can
	// never outlive the block it was made about.
	PendingRepair *RepairRequest
	// PendingFollowUp is a follow-up run a human asked for from `done` or
	// `aborted` (§6, task 027). Unlike a repair request it carries a cursor
	// into its own workflow, because a follow-up may be a whole multi-step
	// workflow and `current_step` stays where the finished run left it
	// (decision 4). It survives the block a failed follow-up step produces
	// and the retry that re-runs it, and is dropped by whatever returns the
	// task to a settled state.
	PendingFollowUp *FollowUpRequest
	// PendingInputJSON is the normalized InputRequest while the task is
	// awaiting_input (§7.4); "" otherwise. TransitionTask clears it on any
	// transition out of awaiting_input.
	PendingInputJSON string
	// AdmitNotBefore holds admission until this instant (§11, task 003); nil
	// means admissible now. Generic on purpose: the scheduler reads one
	// timestamp whatever put it there.
	AdmitNotBefore *time.Time
	// QueuedReason says why a queued task is waiting for something other than
	// a slot — `usage_limit` today. "" is the ordinary queue. It is not
	// BlockReason: that one means "set while blocked" (§14), and a queued task
	// carrying one would lie to every client that keys off it.
	//
	// Both clear on any transition *out of* queued, in TransitionTask rather
	// than in each caller, so a hold cannot outlive the queued period it
	// belongs to.
	QueuedReason string
	// ParentTaskID links a lane to the task whose `fan_out` step spawned it
	// (task 014, §7.6); nil for a root task. ParentStepIndex, LaneID and
	// LaneOrder are set with it and nil/empty without it: the step it came
	// from, the lane's id in that step, and the lane's **declared** position,
	// which is the order the join merges in — completion order would make a
	// re-run conflict differently and break recovery's idempotence
	// (decisions 7, 9).
	//
	// A lane is otherwise an ordinary task in every respect. These four
	// columns are the whole difference (decision 1).
	ParentTaskID    *int64
	ParentStepIndex *int
	LaneID          string
	LaneOrder       int
	// SettledChildrenWatermark is how many **direct** children had settled
	// when the admission that parked this task started (§7.6, task 081
	// decision 1). It is the wake position of a parent parked under a
	// `schedule: eager` fan_out step: the scheduler re-queues it once the
	// live count exceeds this one, which is a predicate that clears itself
	// and so cannot spin the way "a child has settled" does.
	//
	// nil is barrier — every parent parked by a `schedule: barrier` step,
	// and every task that is not parked at all. TransitionTask clears it on
	// any transition out of `awaiting_children`, so it can never describe a
	// parked period other than the one that wrote it.
	SettledChildrenWatermark *int
	// CreatedByTaskID names the task whose agent created this one over MCP
	// (§13.4, task 057 decision 7). NULL — nil — is every task a human, the
	// CLI, the TUI or a `fan_out` step created; provenance is a fact about
	// the MCP path only.
	//
	// It is deliberately not ParentTaskID. That column is what `subtree.go`
	// counts children by for the `awaiting_children` join and what
	// ListTasks's ChildrenExclude filters roots by, so a task placed there
	// out of band would make its creator's `fan_out` step wait on a lane it
	// never spawned. This one is walked by nothing but `mcp.max_depth` and
	// `mcp.max_tasks`.
	CreatedByTaskID *int64
	// GitHubIssue is the issue this task was created from (§5.3, task 035),
	// captured once at creation and nil for every task created without one.
	// It is never re-fetched: every step renders `.Issue` from this snapshot,
	// which is why a step render still cannot fail for an external reason
	// (§8.4). A fan-out lane inherits its parent's copy verbatim.
	GitHubIssue *github.Issue
	// GitHubPull is the pull request this task is linked to (§5.3, task 052),
	// nil for a task no pull request has ever matched.
	//
	// It is a **pointer**, not a snapshot: repo, number, who linked it, and
	// whether a human unlinked it. Everything renderable about the pull
	// request — title, state, draft, merged — is re-fetched on every render,
	// which is the deliberate opposite of GitHubIssue above. A non-nil value
	// with Suppressed set is the record of a human's refusal, not a link; the
	// reconciler reads it so the next tick does not re-apply what a person
	// just removed.
	GitHubPull *github.PullLink
	// WorkflowOrigin is where this task's workflow definition came from
	// (§5.3, task 043), captured once at creation beside WorkflowSnapshot.
	// nil means the origin was not recorded — a task created before migration
	// 0017 — and is reported as `unknown`, never re-derived from today's
	// registry: a re-lookup would report the shadowing this field exists to
	// make visible as though it had always been so.
	WorkflowOrigin *WorkflowOrigin
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
	ArchivedAt     *time.Time
}

// Workflow origin scopes (task 043). The first three mirror workflow.Scope —
// the shadowing walk's three registry scopes — and `derived` is the one a
// registry can never produce: a fan-out lane's steps come from its parent's
// snapshot, resolved at the *parent's* creation (§7.6), so the lane never reads
// a registry at all.
const (
	WorkflowScopeBuiltin = "builtin"
	WorkflowScopeGlobal  = "global"
	WorkflowScopeProject = "project"
	WorkflowScopeDerived = "derived"
)

// WorkflowOrigin is a task's recorded workflow provenance (§5.3, task 043),
// stored as the JSON of `workflow_origin_json`.
//
// It is deliberately its own type rather than workflow.Origin: that one
// describes a registry *entry*, and this one describes a *task*, whose origin
// may be `derived` — a scope no entry has. The registry-backed shapes are
// converted from workflow.Origin at task creation.
//
// Frozen at creation and never recomputed. The digest therefore identifies the
// file version the task was created from, not the bytes the engine executes:
// include expansion (§7.9), fan-out resolution (§7.6) and `edit + retry` all
// rewrite WorkflowSnapshot afterwards, and `edit + retry` is independently
// audited through step_runs.prompt_override / run_override.
type WorkflowOrigin struct {
	// Scope is one of the WorkflowScope* constants above.
	Scope string `json:"scope"`
	// File is the source path relative to its scope root, forward-slashed —
	// `.vincent/workflows/adhoc.yaml`, `workflows/release.yaml`. Empty for
	// `builtin` and `derived`.
	File string `json:"file,omitempty"`
	// Digest is `sha256:<hex>` over the registry entry's source bytes as
	// loaded. Empty for `derived`.
	Digest string `json:"digest,omitempty"`
	// ParentTaskID names the task whose fan_out step spawned this lane; set
	// only for `derived`. Inheriting the parent's file and digest instead
	// would claim the lane's steps came from a file they did not come from.
	ParentTaskID *int64 `json:"parent_task_id,omitempty"`
}

// StepRun is one attempt at executing one step of one task; history is
// append-only (spec §5.4).
type StepRun struct {
	ID        int64
	TaskID    int64
	StepIndex int
	StepID    string
	StepType  string // agent | command | manual | condition | break | fan_out
	Attempt   int    // 1-based
	// Iteration is which pass of an enclosing `loop` produced this row —
	// 1-based, and 0 for every row outside a loop (§7.8, task 016
	// decision 7). Body steps share the loop's StepIndex, so this and StepID
	// together are what tell two of them apart.
	Iteration int
	// LoopItem is the `for_each` item this iteration ran on, empty for a
	// `count:` loop and outside a loop. It is recorded rather than
	// re-derived: it is the loop's extent, not a question, and re-asking it
	// mid-loop would re-index a resumed iteration onto different work
	// (decision 8).
	LoopItem string
	// LoopTotal is how many iterations the admission that wrote this row
	// planned to run: the `count:`, or the resolved `for_each` list length.
	// 0 means no extent was recorded — every row outside a loop, and every
	// row written before the column existed.
	//
	// Like LoopItem it is written once, at insert, and never updated: it is
	// what *that* admission planned, not a running total (migration 0026).
	LoopTotal     int
	State         StepRunState
	Agent         string // adapter name; agent steps only
	Model         string // resolved model as passed to the adapter (spec §8.6); "" = CLI default
	Effort        string // resolved effort as passed to the adapter; "" = CLI default
	PID           *int   // while running
	ProcStartedAt *time.Time
	// ProcIdentity is the platform-native identity of that process, read
	// beside the PID at spawn and compared byte-for-byte by §12.4 recovery
	// (issue #149, migration 0013). It is opaque: nothing parses it. nil
	// means none was journaled — a pre-0013 row, or a spawn whose identity
	// read failed — and recovery then falls back to the ProcStartedAt
	// tolerance.
	ProcIdentity *string
	// ContainerID is the container the step's process ran inside (§16,
	// migration 0021). nil means the host, which is every row of an
	// installation that never sets `container.image`.
	//
	// It is the recovery identity for a containerized run: the host PID a row
	// journals names the `docker exec` client, not the process inside, so
	// §12.4 removes the container instead of killing a PID — after confirming
	// the container still carries the label naming this task.
	ContainerID   *string
	ExitCode      *int
	CheckExitCode *int
	FailureReason string
	// SkipReason says why a `skipped` row is skipped: SkipReasonCondition
	// for a false guard, empty for the human `skip` action (§6, §7.7).
	SkipReason    string
	ResultSummary string
	// StdoutTail is the stdout-only tail of a command step's attempt, and is
	// what §8.4 means by `.Steps.<id>.Result` for a command step (issue #311,
	// migration 0025). ResultSummary keeps *both* streams and stays the
	// human-facing summary; this is the machine-facing one, so a `for_each:`
	// (§7.6, §7.8) splitting `.Result` into items cannot pick up a progress
	// meter or a `Switched to branch …` as an item.
	//
	// nil means none was recorded — a pre-0025 row, and every step type that
	// runs no command — and readers fall back to ResultSummary, so a task
	// already in flight over an upgrade renders as it did. A non-nil empty
	// string is a command that wrote nothing to stdout, which is a different
	// fact and reads as an empty `.Result`.
	StdoutTail *string
	// StatusMessage is what the step said about *itself* (§5.4, task 036):
	// short free text its own process wrote through
	// `POST /v1/tasks/{id}/steps/{step_id}/status`. Empty is the ordinary
	// case — a step with nothing to say says nothing, and the step types that
	// run no process never say anything.
	//
	// It is never written by UpdateStepRun, only by SetStepRunStatus. That is
	// what makes the last live value survive onto the finished row: the
	// actor's own final write does not carry the column, so it cannot clobber
	// a status the step set from another goroutine seconds earlier.
	StatusMessage string
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

// StepRef names one step's run history — the position a `step_runs` row
// belongs to.
//
// It is four values because an index alone stopped identifying a step twice.
// A `parallel` group's sub-steps share the group's StepIndex and are told
// apart by StepID (task 014 decision 16); a `loop` body's steps share the
// loop's StepIndex *and* repeat, and are told apart by Iteration as well
// (§7.8, task 016 decision 7). Outside both, StepID is the step's own id and
// Iteration is 0, so the extra fields cost an ordinary step nothing.
type StepRef struct {
	TaskID    int64
	StepIndex int
	StepID    string
	// Iteration is the 1-based pass of an enclosing loop; 0 outside one.
	Iteration int
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

// RepairRequest is one ad-hoc repair a human asked for from `blocked` (§6,
// task 025): what to tell the agent, and optionally which agent to tell.
//
// Prompt is literal text, never a `text/template` source. It is prose typed
// at a form, and §8.4 renders with `missingkey=error` — a stray `{{` in prose
// would fail the repair before the process started. The surrounding failure
// context is assembled by the daemon, not templated by the user.
//
// BlockReason is the reason the task carried when the repair was launched.
// It rides the request because `applyChange` clears `block_reason` on any
// move off `blocked`, and the repair has to put the *same* reason back when
// it returns the task there: a repair decides nothing about the blocked step.
type RepairRequest struct {
	Prompt      string `json:"prompt"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	BlockReason string `json:"block_reason,omitempty"`
}

// Empty reports whether the request carries no work to do.
func (r RepairRequest) Empty() bool { return r.Prompt == "" }

// Follow-up run forms (§6, task 027 decision 3). The form says what the
// operator asked for; the daemon compiles all three into one workflow, so
// the engine has exactly one shape to execute.
const (
	// FollowUpAgent is a free-form agent prompt.
	FollowUpAgent = "agent"
	// FollowUpCommand is a shell command, run under the daemon's shell
	// (§8.3).
	FollowUpCommand = "command"
	// FollowUpWorkflow is a named workflow from the registry, run against the
	// finished task's worktree instead of a new one.
	FollowUpWorkflow = "workflow"
)

// FollowUpRequest is one follow-up run a human asked for from `done` or
// `aborted` (§6, task 027): what to run, how to select an agent for it, where
// the task must be returned to, and how far through it the daemon has got.
//
// Prompt and Run are literal text, never `text/template` sources, for the
// reason RepairRequest.Prompt is: they are typed at a form, and §8.4 renders
// with `missingkey=error`. The daemon escapes them when it compiles Workflow.
//
// Workflow is the *spliced* follow-up workflow — includes expanded and
// fan-out lanes resolved, exactly as a task's snapshot is at creation (§7.9,
// §7.6). It is stored rather than re-derived so a registry edit mid-follow-up
// cannot mutate a run in flight, which is §5.3's rule applied to this second
// cursor.
type FollowUpRequest struct {
	// Form is one of FollowUpAgent, FollowUpCommand or FollowUpWorkflow. It
	// is what the operator asked for, kept for clients to render; execution
	// reads Workflow alone.
	Form string `json:"form"`
	// Prompt, Run and WorkflowName are what was typed, one per form.
	Prompt       string `json:"prompt,omitempty"`
	Run          string `json:"run,omitempty"`
	WorkflowName string `json:"workflow_name,omitempty"`
	// Workflow is the compiled, spliced follow-up workflow's YAML.
	Workflow string `json:"workflow"`
	// Agent, Model and Effort stand in for the step level of §8.6's chain,
	// exactly as a repair's do — except that an explicit field on a step of a
	// workflow-form follow-up still outranks them, because that is what §8.6
	// already says about a step field (decision 12).
	Agent  string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Origin is the state the follow-up was launched from, and the state the
	// task is returned to when it ends (decision 5). A follow-up decides
	// nothing about the task's verdict.
	Origin TaskState `json:"origin"`
	// Round is 1-based and is what places this run's rows in the cursor space
	// past the snapshot's last step: they sit at
	// `len(snapshot.Steps) + Round - 1` (decision 2).
	Round int `json:"round"`
	// Cursor is how many steps of Workflow have finished — the follow-up's
	// own step cursor (decision 4). `current_step` is left where the finished
	// run put it and is never walked by a follow-up.
	Cursor int `json:"cursor"`
	// Abandoned marks a follow-up a human ended with `skip` from the block a
	// failed step produced (decision 6). The record is kept rather than
	// dropped because Origin is still needed: the next admission restores
	// `done` or `aborted` without running anything.
	Abandoned bool `json:"abandoned,omitempty"`
}

// Empty reports whether the request carries no work to do. A request with no
// compiled workflow can neither run nor say where to return the task, which
// is the same thing.
func (r FollowUpRequest) Empty() bool { return r.Workflow == "" }

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
	// OpenStepRuns is how many of the task's step runs are still marked
	// `running`. A queued task must have none: §12.4 finalizes the previous
	// attempt before the task returns to the queue. Any other number means
	// the task was never reconciled, and admitting it would start a second
	// attempt against a first one the database still calls live (issue #142).
	OpenStepRuns int
}

// Unreconciled is one task whose state and its step runs contradict each
// other — a step run still marked `running` under a task that cannot possibly
// be executing (§12.4). See UnreconciledTasks for what "cannot possibly" means
// here.
type Unreconciled struct {
	TaskID       int64
	State        TaskState
	OpenStepRuns int
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

// Chat is a titled conversation with an agent, scoped to a project and
// running in its own git worktree and `vincent/{id}-{slug}` branch (spec
// §5.5, task 063). It is a first-class entity beside Task, not a task with a
// different shape: it has no workflow snapshot, no step ledger and no §6
// lifecycle, and it never appears on the task board.
type Chat struct {
	ID        int64
	ProjectID int64
	Title     string
	State     chatstate.State
	// Agent is the adapter this chat talks to. It is fixed at creation and
	// must be one that can resume its own session (§9.1): a chat whose
	// adapter changed mid-conversation would resume an id another CLI never
	// issued.
	Agent          string
	Model          string
	Effort         string
	PermissionMode string
	Branch         string
	BaseBranch     string
	BaseSHA        string
	// WorktreePath is the §10 claim. Empty once archived, which is what
	// takes the directory out of gc's claim set.
	WorktreePath string
	// SessionID is the agent CLI's own conversation id, resumed on the next
	// turn (§7.3, amended for chats). Empty before the first turn finishes.
	SessionID string
	// PendingInput is the §7.4 request this chat is waiting on, verbatim as
	// the API renders it. Non-nil exactly in `awaiting_input`.
	PendingInput json.RawMessage
	// HandoffTaskID names the task this chat was handed off to (task 074).
	// It is the one authoritative edge between the two records; a task's
	// `source_chat_id` is this column read backwards. Non-nil exactly in
	// `handed_off`, unless that task has since been deleted.
	HandoffTaskID *int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ChatTurn is one exchange: a human message and the agent run it produced
// (spec §5.5, §14). Its accounting columns are step_runs' — tokens, cost,
// duration, pid, exit code, proc identity — because closing the accounting
// gap is half of what chats are for; `step_runs` itself is untouched, and
// `step_runs.task_id` stays NOT NULL.
type ChatTurn struct {
	ID     int64
	ChatID int64
	// Seq is 1-based position in the conversation, unique per chat.
	Seq    int
	Prompt string
	State  chatstate.TurnState
	// FailReason is the shared snake_case vocabulary internal/taskrun and
	// internal/worktree own — `session_lost`, `timeout`, `agent_error`. A
	// reason means the same thing wherever it originated.
	FailReason   string
	ErrorMessage string
	ResultText   string
	SessionID    string
	InputTokens  int64
	OutputTokens int64
	CostUSD      *float64
	ExitCode     *int
	PID          *int
	ProcIdentity *string
	StartedAt    time.Time
	EndedAt      *time.Time
	DurationMS   *int64
}

// ChatWorktreeClaim is one chat's claim on a data-root directory (§10). It
// mirrors WorktreeClaim, and gc unions the two sets: the roots are shared, so
// a chat's worktree that no set named would be deleted as a stray.
type ChatWorktreeClaim struct {
	ChatID int64
	Path   string
	State  chatstate.State
}
