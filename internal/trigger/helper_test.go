package trigger

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"
)

// The poll command in these tests is this test binary re-executed, so no test
// depends on /bin/sh — Windows runs the same child (the notify package's
// pattern, task 046).
// The sentinel is a bare word: the test binary's flag parsing stops at the
// first non-flag argument, so everything from it on reaches os.Args
// untouched.
const helperSentinel = "trigger-helper"

func helperArgv(t *testing.T, mode string, extra ...string) []string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return append([]string{self, "-test.run=TestTriggerHelperProcess", helperSentinel, mode}, extra...)
}

// TestTriggerHelperProcess is not a test: it is the poll command, selected by
// -test.run and told what to do by the argv after the sentinel.
func TestTriggerHelperProcess(t *testing.T) {
	i := slices.Index(os.Args, helperSentinel)
	if i < 0 {
		t.Skip("not running as the trigger's helper child")
	}
	args := os.Args[i+1:]
	switch args[0] {
	case "print":
		// Each remaining argument is printed as one stdout line; the cursor
		// the daemon handed over goes to stderr so a test can read it back
		// from the log.
		fmt.Fprintln(os.Stderr, "cursor="+os.Getenv(CursorEnv))
		for _, l := range args[1:] {
			fmt.Println(l)
		}
	case "fail":
		fmt.Fprintln(os.Stderr, "helper: deliberate failure")
		os.Exit(3)
	case "hang":
		// A grandchild that inherits stdout and outlives its parent unless
		// the whole tree is killed: without the tree kill, Wait parks until
		// WaitDelay.
		self, _ := os.Executable()
		gc := exec.Command(self, "-test.run=TestTriggerHelperProcess", helperSentinel, "sleep")
		gc.Stdout = os.Stdout
		_ = gc.Start()
		time.Sleep(10 * time.Minute)
	case "sleep":
		time.Sleep(10 * time.Minute)
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", args[0])
		os.Exit(2)
	}
	os.Exit(0)
}
