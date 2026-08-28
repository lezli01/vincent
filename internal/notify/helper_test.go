package notify

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// The notifier's child is a re-exec of the test binary rather than a script:
// there is no shell to assume on Windows, `sleep` and `cat` are not portable,
// and a Go helper runs identically on all three platforms. The guard is the
// `--` sentinel in argv — a normal `go test` invocation never carries one —
// so no environment variable has to survive the environment policy the
// notifier applies to its children.
const helperSentinel = "--"

// helperArgv builds the argv for one helper mode. dir is where the helper
// writes; each invocation writes its own file, so concurrent children never
// race over one path.
func helperArgv(t *testing.T, mode, dir string, extra ...string) []string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	argv := []string{self, "-test.run=TestNotifyHelperProcess", helperSentinel, mode, dir}
	return append(argv, extra...)
}

// helperFiles returns the contents of every file the helper wrote in dir.
func helperFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read helper dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		out = append(out, string(b))
	}
	slices.Sort(out)
	return out
}

// TestNotifyHelperProcess is not a test: it is the notifier's child process,
// selected by -test.run and told what to do by the argv after `--`.
func TestNotifyHelperProcess(t *testing.T) {
	args := os.Args
	i := slices.Index(args, helperSentinel)
	if i < 0 {
		t.Skip("not running as the notifier's helper child")
	}
	args = args[i+1:]
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "helper: want a mode and a directory")
		os.Exit(2)
	}
	mode, dir := args[0], args[1]
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: read stdin: %v\n", err)
		os.Exit(2)
	}
	write := func(name string, content []byte) {
		if werr := os.WriteFile(filepath.Join(dir, name), content, 0o600); werr != nil {
			fmt.Fprintf(os.Stderr, "helper: write: %v\n", werr)
			os.Exit(2)
		}
	}
	unique := strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	switch mode {
	case "capture":
		// The remaining argv is echoed back beside the envelope so the test can
		// assert the child was spawned with byte-identical arguments.
		write(unique+".json", body)
		write(unique+".argv", []byte(fmt.Sprint(args[2:])))
	case "fail":
		write(unique+".json", body)
		fmt.Fprintln(os.Stderr, "helper: deliberate failure")
		os.Exit(3)
	case "hang":
		write(unique+".json", body)
		// A long sleep, not `select {}`: an empty select parks every goroutine
		// and the runtime's deadlock detector would exit the process, which is
		// the opposite of hanging. The notifier's timeout is what ends this.
		time.Sleep(10 * time.Minute)
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", mode)
		os.Exit(2)
	}
	os.Exit(0)
}
