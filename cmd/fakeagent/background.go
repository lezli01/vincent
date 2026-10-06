package main

import (
	"bufio"
	"os"
	"time"
)

// The `background` scenario (task 133): the model starts work in the
// background and ends its turn promising to report, the way claude 2.1.289
// writes it (internal/agent/claude/testdata/stream_background_2.1.289.jsonl).
// What happens next is the CLI's, and it depends on stdin, exactly as the
// real one's does:
//
//   - stdin still open when the work finishes: the work's task_updated and
//     task_notification, a woken model turn, and a second result;
//   - stdin closed first: the work is killed — task_updated `killed`,
//     task_notification `stopped` — and the process exits on the first
//     result, which is the hang-up vincent used to cause on every run.
//
// FAKEAGENT_BACKGROUND_MS is how long the work takes (default 300 ms).
// FAKEAGENT_BACKGROUND_LINGER=1 starts a second task that never finishes on
// its own, the dev server nobody stopped, so only closing stdin ends the run.

const (
	fakeBackgroundTask   = "bfake0001"
	fakeBackgroundLinger = "bfake0002"
)

func background(rd *bufio.Reader) {
	lingering := os.Getenv("FAKEAGENT_BACKGROUND_LINGER") == "1"
	emitTaskStarted(fakeBackgroundTask, "Run the test suite")
	if lingering {
		emitTaskStarted(fakeBackgroundLinger, "Serve the app")
	}
	emitText("the suite is running in the background; I'll report when it finishes")
	emitResult("waiting on the suite", 100, 42, 0.01)

	work := envMillis("FAKEAGENT_BACKGROUND_MS")
	if work == 0 {
		work = 300 * time.Millisecond
	}
	hangup := stdinClosed(rd)
	select {
	case <-hangup:
		emitTaskSettled(fakeBackgroundTask, "killed", "stopped")
		if lingering {
			emitTaskSettled(fakeBackgroundLinger, "killed", "stopped")
		}
		return
	case <-time.After(work):
	}
	emitTaskSettled(fakeBackgroundTask, "completed", "completed")
	emitText("the suite passed")
	emitResult("the suite passed", 10, 5, 0.02)
	<-hangup
	if lingering {
		emitTaskSettled(fakeBackgroundLinger, "killed", "stopped")
	}
}

// stdinClosed reports when stdin reaches EOF, which is how the CLI learns no
// further user message is coming. A plain run has no stdin left to read: its
// prompt was all of it, so it is closed already.
func stdinClosed(rd *bufio.Reader) <-chan struct{} {
	done := make(chan struct{})
	if rd == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		for {
			if _, err := rd.ReadString('\n'); err != nil {
				return
			}
		}
	}()
	return done
}

func emitTaskStarted(id, description string) {
	emit(map[string]any{
		"type": "system", "subtype": "task_started", "task_id": id,
		"tool_use_id": "toolu_" + id, "description": description,
		"task_type": "local_bash", "is_backgrounded": true,
	})
}

// emitTaskSettled is the pair claude writes when a task ends: the patch,
// then the notification the model is woken with.
func emitTaskSettled(id, patch, notification string) {
	emit(map[string]any{
		"type": "system", "subtype": "task_updated", "task_id": id,
		"patch": map[string]any{"status": patch},
	})
	emit(map[string]any{
		"type": "system", "subtype": "task_notification", "task_id": id,
		"tool_use_id": "toolu_" + id, "status": notification,
	})
}

// emitResult is a success result as a held run writes them: usage is the
// turn's own, total_cost_usd the process's running total.
func emitResult(text string, inTok, outTok int64, cost float64) {
	emit(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": text, "total_cost_usd": cost,
		"usage": map[string]int64{"input_tokens": inTok, "output_tokens": outTok},
	})
}
