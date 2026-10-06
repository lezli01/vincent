package claude

// Background work across a `result` (spec §9.2, task 133). claude's Bash tool
// takes `run_in_background`, its Agent tool backgrounds a subagent, and
// Monitor watches a command, all of them promising the model it will be
// woken when the work finishes. The model takes the promise at its word and
// ends its turn — "the suite is running, I'll report when it's done". In
// input mode the CLI keeps that promise: with stdin still open it writes the
// work's `task_notification`, starts a model turn of its own and writes a
// second `result` (captured against 2.1.289 in
// testdata/stream_background_2.1.289.jsonl). With stdin closed it exits,
// killing the work, and the run ends on the "I'll report" text.
//
// So the read loop asks this tracker whether any work is still out before it
// closes stdin on a result. Every `task_started` opens an entry, by its
// `task_id`; a `task_notification` or a `task_updated` patch to a terminal
// status closes it. The tracker does not care what the work is: a shell, a
// subagent and a monitor all announce themselves the same way, and a
// synchronous subagent's entry opens and closes before the result it
// belongs to, so it never holds anything open.

import "encoding/json"

// backgroundLine is the part of a `system` task line the tracker reads. The
// stream parser's streamLine has no task_id, because nothing it normalizes
// needs one; this is the only reader that does.
type backgroundLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	TaskID  string `json:"task_id"`
	Patch   struct {
		Status string `json:"status"`
	} `json:"patch"`
}

// settledStatus is every `task_updated` patch status that ends a task.
// `running` and `pending` are the others claude writes, and neither does.
var settledStatus = map[string]bool{
	"completed": true,
	"failed":    true,
	"killed":    true,
	"stopped":   true,
}

// backgroundTasks is the set of task ids started and not yet settled. Only
// the read loop touches it, so it needs no lock. The zero value is ready.
type backgroundTasks map[string]bool

// observe updates the set from one stream line. Lines that are not task
// lines, and task lines without a task_id, leave it alone.
func (b *backgroundTasks) observe(raw []byte) {
	var line backgroundLine
	if err := json.Unmarshal(raw, &line); err != nil || line.Type != "system" || line.TaskID == "" {
		return
	}
	switch line.Subtype {
	case "task_started":
		if *b == nil {
			*b = backgroundTasks{}
		}
		(*b)[line.TaskID] = true
	case "task_notification":
		delete(*b, line.TaskID)
	case "task_updated":
		if settledStatus[line.Patch.Status] {
			delete(*b, line.TaskID)
		}
	}
}

// outstanding is how many started tasks have not settled.
func (b backgroundTasks) outstanding() int { return len(b) }
