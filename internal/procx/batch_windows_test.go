//go:build windows

package procx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBatchCmdLineWrapsOnlyBatchFiles pins the line itself: os/exec's own
// escaping inside one outer pair of quotes for a `.cmd`/`.bat`, and no
// CmdLine at all for anything else.
func TestBatchCmdLineWrapsOnlyBatchFiles(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{`C:\npm prefix\copilot.cmd`, `""C:\npm prefix\copilot.cmd" --x "{\"a\":\"b c\"}""`},
		{`C:\npm prefix\copilot.BAT`, `""C:\npm prefix\copilot.BAT" --x "{\"a\":\"b c\"}""`},
		{`C:\npm prefix\copilot.exe`, ""},
	} {
		cmd := &exec.Cmd{Path: tc.path, Args: []string{tc.path, "--x", `{"a":"b c"}`}}
		setSysProcAttr(cmd)
		if got := cmd.SysProcAttr.CmdLine; got != tc.want {
			t.Errorf("%s: CmdLine = %s, want %s", tc.path, got, tc.want)
		}
		assertNoWindow(t, cmd)
	}
}

// TestBatchFileInSpacedDirRunsWithQuotedArgs is review F1's failure through
// the real cmd.exe: a shim under a directory with a space, handed an
// argument with a space and embedded quotes. Without the outer quotes,
// `cmd /c` strips the last quote of the line instead of the first pair's
// closing one, and the shim's own path splits at its space ("is not
// recognized"). With them, the shim runs and sees the arguments verbatim.
func TestBatchFileInSpacedDirRunsWithQuotedArgs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "npm prefix")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "echo.cmd")
	if err := os.WriteFile(shim, []byte("@ECHO %*\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shim, "--x", `{"a":"b c"}`)
	setSysProcAttr(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s: %v\n%s", shim, err, out)
	}
	if got, want := strings.TrimSpace(string(out)), `--x "{\"a\":\"b c\"}"`; got != want {
		t.Errorf("shim saw %s, want %s", got, want)
	}
}
