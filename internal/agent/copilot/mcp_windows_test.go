package copilot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/mcp/mcptest"
)

// TestStartMCPCallbackThroughCmdShim is the Windows proof task 138
// decision 6 waits on: the real CLI is an npm `copilot.cmd` shim, so cmd.exe
// re-parses the command line before the program sees it, and inline JSON —
// full of `"` — has to come through that intact. Launching the fake agent's
// `.exe` directly would never touch cmd.exe and prove nothing, so this wraps
// it in a shim of the same shape (`"<program>" %*`) and points the adapter at
// the `.cmd`. A recorded step_status is the proof the fake parsed the config
// it was handed.
func TestStartMCPCallbackThroughCmdShim(t *testing.T) {
	exe := agenttest.BuildFakeAgent(t)
	// A space in the directory, as under "C:\Program Files\nodejs". The
	// quoted shim path plus the quoted JSON put more than two `"` on the
	// line, and `cmd /c` then drops the last one. Without procx.BatchCmdLine
	// the path splits at this space (review F1), so the space must stay.
	dir := filepath.Join(t.TempDir(), "npm prefix")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "copilot.cmd")
	body := "@ECHO off\r\n\"" + exe + "\" %*\r\n"
	if err := os.WriteFile(shim, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := mcptest.New(t)
	h := startMCPCallback(t, New(func() string { return shim }), srv, "copilot called back through cmd.exe")
	checkMCPCallback(t, h, srv, "copilot called back through cmd.exe")
}
