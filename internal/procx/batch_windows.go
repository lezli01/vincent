//go:build windows

package procx

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// BatchCmdLine makes a `.cmd` or `.bat` target survive cmd.exe's own parse.
// Call it after SysProcAttr is set (NoWindow, setSysProcAttr): it fills in
// SysProcAttr.CmdLine and leaves every other target alone.
//
// CreateProcess runs a batch file as `cmd.exe /c <command line>`, and os/exec
// gives a batch file no quoting of its own (golang/go#17149). When the line
// holds more than two `"`, `cmd /c` drops the first and the *last* of them
// (`cmd /?`). Go quotes the program path when it has a space, as under an npm
// prefix in "C:\Program Files\nodejs", and quotes any argument with a space,
// such as copilot's inline MCP config with its `Bearer <token>` header. The
// last quote then falls inside the arguments, and the program path splits
// at its space: `'C:\...\npm' is not recognized` (review F1, task 138.8). One
// outer pair around the whole line is what `cmd /c` strips instead, so the
// line Go would have built reaches the batch file unchanged. Rust's std does
// the same since BatBadBut.
//
// This does nothing about cmd.exe's metacharacters (`&|<>^%`). The arguments
// vincent passes an agent shim carry none: loopback URLs, base64url tokens,
// option values from the adapters' catalogs. The prompt goes over stdin.
func BatchCmdLine(cmd *exec.Cmd) {
	if !isBatch(cmd.Path) {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = batchCmdLine(cmd.Args)
}

// batchCmdLine is the line os/exec would build from args, each argument
// escaped by the same syscall.EscapeArg, then wrapped in one outer pair of
// quotes for `cmd /c` to strip.
func batchCmdLine(args []string) string {
	escaped := make([]string, len(args))
	for i, a := range args {
		escaped[i] = syscall.EscapeArg(a)
	}
	return `"` + strings.Join(escaped, " ") + `"`
}

func isBatch(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		return true
	}
	return false
}
