package apiclient_test

import (
	"os"
	"testing"
)

// TestMain drops the agent markers' variables before any test runs. This
// suite is itself run from inside a vincent step, whose environment carries
// VINCENT_TASK_ID, and every client the live tests build would then send
// the step marker and be judged an agent by the forge-write guard (task
// 137.7). A test that wants a marked client sets the variable itself.
func TestMain(m *testing.M) {
	for _, env := range []string{"VINCENT_TASK_ID", "VINCENT_CHAT_ID"} {
		_ = os.Unsetenv(env)
	}
	os.Exit(m.Run())
}
