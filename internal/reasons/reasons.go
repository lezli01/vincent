package reasons

import (
	"maps"
	"slices"
)

// Explanation is what one reason means to a human.
type Explanation struct {
	// Title is a short lowercase label: "check failed", not "check_failed".
	Title string
	// Meaning is one sentence saying what happened.
	Meaning string
	// Actions are the §6 human actions that answer the reason, most useful
	// first, as their wire strings. Empty for a reason that is terminal or
	// that the engine resolves on its own.
	Actions []string
	// DocAnchor is a path under docs/ and a heading fragment, such as
	// "guides/troubleshooting.md#worktree_dirty". Empty only for a reason
	// this catalogue does not know.
	DocAnchor string
}

// The anchor every reason without a troubleshooting heading of its own
// points at: the lifecycle reference's table, which lists them all.
const lifecycle = "reference/task-lifecycle.md#failure-reasons"

func troubleshooting(fragment string) string {
	return "guides/troubleshooting.md#" + fragment
}

// Action lists, in task 025 decision 8's order: retry first, then skip,
// then repair, then the task 119 chat where the worktree exists to chat in.
// Entries share these slices; Explain clones before handing one out.
var (
	stepFailed    = []string{"retry", "skip", "repair", "chat"}
	retrySkip     = []string{"retry", "skip"}
	retryOnly     = []string{"retry"}
	inWorktree    = []string{"retry", "repair", "chat"}
	cannotRecover = []string{"cancel"}
)

// catalogue is every task and step reason, keyed by its §18 wire string.
var catalogue = map[string]Explanation{
	// Step failures (internal/taskrun).
	"check_failed": {
		Title: "check failed", Meaning: "The step ran, but its check command exited non-zero.",
		Actions: stepFailed, DocAnchor: lifecycle,
	},
	"nonzero_exit": {
		Title: "command exited non-zero", Meaning: "A command step's process exited with a non-zero status.",
		Actions: stepFailed, DocAnchor: lifecycle,
	},
	"agent_error": {
		Title: "agent reported an error", Meaning: "The agent CLI's own event stream reported that the run failed.",
		Actions: stepFailed, DocAnchor: lifecycle,
	},
	"agent_unavailable": {
		Title: "agent not available", Meaning: "The agent CLI could not be found or started on this machine.",
		Actions: retrySkip, DocAnchor: troubleshooting("an-agent-cli-is-not-found"),
	},
	"agent_unauthenticated": {
		Title: "agent not logged in", Meaning: "The agent CLI is installed but not logged in, so it refused to run.",
		Actions: retrySkip, DocAnchor: troubleshooting("agent_unauthenticated"),
	},
	"usage_limit": {
		Title: "usage limit reached", Meaning: "The agent's usage quota for the current window is spent; this costs no retry.",
		Actions: retryOnly, DocAnchor: troubleshooting("usage_limit--do-nothing-unless-you-asked-to-be-told"),
	},
	"retry_backoff": {
		Title: "waiting to retry", Meaning: "The step failed and its next attempt is waiting out the workflow's retry backoff.",
		DocAnchor: troubleshooting("retry_backoff--also-do-nothing-but-for-a-different-reason"),
	},
	"timeout": {
		Title: "timed out", Meaning: "The attempt ran past its timeout and was killed.",
		Actions: stepFailed, DocAnchor: lifecycle,
	},
	"input_timeout": {
		Title: "question went unanswered", Meaning: "The agent asked a question that nobody answered within the input timeout.",
		Actions: stepFailed, DocAnchor: lifecycle,
	},
	"input_protocol_error": {
		Title: "unreadable agent question", Meaning: "The agent sent a control message vincent could not parse, so the attempt failed instead of hanging.",
		Actions: stepFailed, DocAnchor: lifecycle,
	},
	"template_error": {
		Title: "template error", Meaning: "A template in the step failed to render before any process started.",
		Actions: retrySkip, DocAnchor: troubleshooting("template_error"),
	},
	"condition_error": {
		Title: "condition error", Meaning: "The step's if: guard failed to render or rendered neither true nor false; it is not retried automatically.",
		Actions: retrySkip, DocAnchor: troubleshooting("condition_error"),
	},
	"fan_out_invalid": {
		Title: "invalid fan-out list", Meaning: "A derived fan-out list is not a valid lane list, so no lane was spawned.",
		Actions: stepFailed, DocAnchor: lifecycle,
	},
	"fan_out_limit": {
		Title: "fan-out too wide", Meaning: "A derived fan-out list is longer than max_lanes or fan_out.max_tasks allows, so no lane was spawned.",
		Actions: stepFailed, DocAnchor: lifecycle,
	},
	"loop_limit": {
		Title: "loop limit reached", Meaning: "The loop cannot run within its max_iterations, so it stopped instead of truncating.",
		Actions: stepFailed, DocAnchor: troubleshooting("loop_limit"),
	},
	"restricted_unsupported": {
		Title: "cannot restrict here", Meaning: "The step asked to run restricted, and its agent cannot restrict on this platform.",
		Actions: retrySkip, DocAnchor: troubleshooting("restricted_unsupported"),
	},
	"mcp_unsupported": {
		Title: "no mcp support", Meaning: "The agent cannot be given vincent's MCP server for this step, so the step did not run without it.",
		Actions: retrySkip, DocAnchor: troubleshooting("mcp_unsupported"),
	},
	"transcript_limit": {
		Title: "transcript too large", Meaning: "The attempt's output passed transcript_max_bytes and the run was killed.",
		Actions: stepFailed, DocAnchor: troubleshooting("transcript_limit"),
	},
	"cost_limit": {
		Title: "cost limit reached", Meaning: "The task has spent past its cost cap; raise the cap before retrying.",
		Actions: retryOnly, DocAnchor: troubleshooting("cost_limit--raise-the-cap-and-retry"),
	},
	"tree_cost_limit": {
		Title: "tree cost limit reached", Meaning: "The task's whole fan-out tree has spent past max_tree_cost_usd; raise it before retrying.",
		Actions: retryOnly, DocAnchor: troubleshooting("tree_cost_limit--raise-the-tree-cap-and-retry-the-parent"),
	},
	"transcript_io_error": {
		Title: "transcript write failed", Meaning: "The attempt's transcript could not be written, often because the disk is full.",
		Actions: stepFailed, DocAnchor: troubleshooting("transcript_io_error--check-the-disk"),
	},
	"agent_protocol_error": {
		Title: "agent stream unreadable", Meaning: "Vincent could not read the agent's output to the end, so the transcript is missing lines.",
		Actions: stepFailed, DocAnchor: troubleshooting("agent_protocol_error--vincents-reader-not-the-clis-fault"),
	},
	"shell_unavailable": {
		Title: "shell not installed", Meaning: "The step pins a shell that is not installed on this machine.",
		Actions: retrySkip, DocAnchor: troubleshooting("shell_unavailable"),
	},
	"container_image_unavailable": {
		Title: "container image unavailable", Meaning: "The task's container image is not on this machine and could not be pulled.",
		Actions: retryOnly, DocAnchor: troubleshooting("container_image_unavailable"),
	},
	"container_unavailable": {
		Title: "container runtime unavailable", Meaning: "The container runtime is gone or refused to create the container, so the step was not run on the host instead.",
		Actions: retryOnly, DocAnchor: troubleshooting("container_unavailable"),
	},
	"rejected": {
		Title: "rejected", Meaning: "A human rejected the task at a manual gate.",
		Actions: retryOnly, DocAnchor: lifecycle,
	},
	"canceled": {
		Title: "canceled", Meaning: "A human cancelled the task.",
		DocAnchor: lifecycle,
	},
	"invalid_snapshot": {
		Title: "invalid workflow snapshot", Meaning: "The workflow snapshot stored with the task cannot be used.",
		Actions: cannotRecover, DocAnchor: lifecycle,
	},
	"platform_unsupported": {
		Title: "wrong platform", Meaning: "The task's workflow is restricted to platforms this machine is not.",
		Actions: cannotRecover, DocAnchor: troubleshooting("a-workflow-is-listed-but-cannot-be-selected--platform_unsupported"),
	},
	"input_unsupported": {
		Title: "agent cannot take questions", Meaning: "The step requires mid-run input, and its agent cannot stop and ask.",
		Actions: retrySkip, DocAnchor: troubleshooting("an-agent-cannot-be-picked-for-a-workflow--input_unsupported"),
	},
	"merge_conflict": {
		Title: "merge conflict", Meaning: "Merging a fan-out lane conflicted, and the worktree is left conflicted for you to resolve.",
		Actions: inWorktree, DocAnchor: lifecycle,
	},
	"lane_failed": {
		Title: "lane failed", Meaning: "A fan-out lane was cancelled or ended without finishing, so nothing of that round was merged.",
		Actions: retryOnly, DocAnchor: lifecycle,
	},
	"interrupted": {
		Title: "interrupted", Meaning: "The daemon stopped mid-step; the step re-runs on its own and costs no retry.",
		DocAnchor: lifecycle,
	},
	"internal_error": {
		Title: "internal error", Meaning: "Vincent hit a bug of its own.",
		Actions: retryOnly, DocAnchor: troubleshooting("reporting-a-bug"),
	},

	// Worktree-layer reasons (internal/worktree).
	"project_path_missing": {
		Title: "project path missing", Meaning: "The project's repository is not at its recorded path.",
		Actions: retryOnly, DocAnchor: troubleshooting("base_branch_missing--project_path_missing"),
	},
	"base_branch_missing": {
		Title: "base branch missing", Meaning: "The branch the task was to start from does not exist.",
		Actions: retryOnly, DocAnchor: troubleshooting("base_branch_missing--project_path_missing"),
	},
	"branch_exists": {
		Title: "branch already exists", Meaning: "The branch this task would create already exists from an earlier run.",
		Actions: retryOnly, DocAnchor: troubleshooting("branch_exists--worktree_path_occupied"),
	},
	"branch_name_invalid": {
		Title: "invalid branch name", Meaning: "The branch name the task would use is not a valid git branch name.",
		Actions: retryOnly, DocAnchor: lifecycle,
	},
	"worktree_dirty": {
		Title: "worktree has uncommitted changes", Meaning: "The task's worktree has uncommitted changes where vincent needs it clean.",
		Actions: inWorktree, DocAnchor: troubleshooting("worktree_dirty"),
	},
	"worktree_missing": {
		Title: "worktree missing", Meaning: "The task's worktree is no longer on disk.",
		Actions: retryOnly, DocAnchor: lifecycle,
	},
	"worktree_path_occupied": {
		Title: "worktree path occupied", Meaning: "Something already occupies the directory the task's worktree would use.",
		Actions: retryOnly, DocAnchor: troubleshooting("branch_exists--worktree_path_occupied"),
	},
	"git_error": {
		Title: "git error", Meaning: "A git command vincent ran for the task failed.",
		Actions: retryOnly, DocAnchor: lifecycle,
	},
	"pull_fetch_failed": {
		Title: "pull request fetch failed", Meaning: "The pull request's head could not be fetched.",
		Actions: retryOnly, DocAnchor: troubleshooting("pull_fetch_failed--pull_branch_diverged--pull_branch_checked_out"),
	},
	"pull_branch_diverged": {
		Title: "pull request branch diverged", Meaning: "A local branch of the pull request's name carries commits its head does not.",
		Actions: retryOnly, DocAnchor: troubleshooting("pull_fetch_failed--pull_branch_diverged--pull_branch_checked_out"),
	},
	"pull_branch_checked_out": {
		Title: "pull request branch checked out", Meaning: "The pull request's branch is already checked out in another worktree.",
		Actions: retryOnly, DocAnchor: troubleshooting("pull_fetch_failed--pull_branch_diverged--pull_branch_checked_out"),
	},
	"adopt_branch_missing": {
		Title: "existing branch missing", Meaning: "The existing branch the task was created on is gone.",
		Actions: retryOnly, DocAnchor: troubleshooting("adopt_branch_missing--adopt_branch_diverged--adopt_branch_checked_out"),
	},
	"adopt_branch_diverged": {
		Title: "existing branch diverged", Meaning: "The existing branch and its upstream have each moved, so nothing was moved.",
		Actions: retryOnly, DocAnchor: troubleshooting("adopt_branch_missing--adopt_branch_diverged--adopt_branch_checked_out"),
	},
	"adopt_branch_checked_out": {
		Title: "existing branch in use", Meaning: "The existing branch is checked out in another of vincent's worktrees.",
		Actions: retryOnly, DocAnchor: troubleshooting("adopt_branch_missing--adopt_branch_diverged--adopt_branch_checked_out"),
	},

	// The skip reason (internal/store).
	"condition": {
		Title: "condition was false", Meaning: "The step's if: guard rendered false, so the step was skipped.",
		DocAnchor: lifecycle,
	},
}

// Explain returns what reason means. An unknown reason explains as itself —
// its raw string as the Title and nothing else — so a client older than the
// daemon it talks to shows what it showed before this catalogue existed.
func Explain(reason string) Explanation {
	e, ok := catalogue[reason]
	if !ok {
		return Explanation{Title: reason}
	}
	e.Actions = slices.Clone(e.Actions)
	return e
}

// Known returns every reason the catalogue explains, sorted.
func Known() []string {
	return slices.Sorted(maps.Keys(catalogue))
}
