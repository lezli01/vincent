package workflow

import (
	"encoding/json"
	"fmt"
)

// The merge-back task's workflow identity (§5.6, §13.2, task 134 decision
// 11, 134.14). A merge-back is a daemon-created task whose snapshot is
// synthesized rather than taken from the registry, so its name and its one
// step id are reserved: both start with an underscore, which no registry
// file name or authored step id may, so neither can collide with a workflow
// somebody wrote. RepairStepID (internal/taskrun) is reserved the same way.
const (
	MergeBackName   = "__merge_back"
	MergeBackStepID = "__merge_back"
	// MergeBackBranchEnv names the snapshot's record of the side branch the
	// merge-back merges. It lives in the snapshot, not only on the source
	// row, so the task still knows what it was for after the source is
	// deleted.
	MergeBackBranchEnv = "VINCENT_MERGE_SOURCE_BRANCH"
)

// MergeBackTitle is a merge-back task's title, and its merge commit's
// message. It differs from a fan_out lane's message, so `diff?by=lane`
// never reads a merge-back as a lane.
func MergeBackTitle(sideTaskID, issueID int64) string {
	return fmt.Sprintf("Merge task %d into issue #%d", sideTaskID, issueID)
}

// MergeBackSource is the synthesized one-step snapshot of a merge-back task.
// The step is a `command` step in shape only: the engine routes the
// reserved id to its own executor before any step type is looked at, and
// the `run:` line is what the step does, written for a reader of the
// snapshot.
func MergeBackSource(sideTaskID, issueID int64, sideBranch string) string {
	title := MergeBackTitle(sideTaskID, issueID)
	return "# Synthesized by vincent: merges a finished side task into its issue's\n" +
		"# main worktree (spec §5.6). Not a registry workflow.\n" +
		"name: " + MergeBackName + "\n" +
		"description: " + yamlString(title) + "\n" +
		"steps:\n" +
		"  - id: " + MergeBackStepID + "\n" +
		"    name: " + yamlString(title) + "\n" +
		"    type: command\n" +
		"    max_retries: 0\n" +
		"    run: " + yamlString(EscapeTemplate("git merge --no-ff refs/heads/"+sideBranch)) + "\n" +
		"    env:\n" +
		"      " + MergeBackBranchEnv + ": " + yamlString(EscapeTemplate(sideBranch)) + "\n"
}

// yamlString quotes s as a YAML double-quoted scalar. A JSON string is one.
func yamlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
