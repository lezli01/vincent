package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Cursor has no per-run MCP flag: `cursor-agent mcp` reads `.cursor/mcp.json`
// in the workspace, or `~/.cursor/mcp.json` globally (spec §9.7, task 057
// decision 8). So the adapter writes the workspace file into the task worktree
// before the run and removes it after — workspace-scoped and per-task, so
// nothing here touches the user's global cursor config.
//
// The helpers live in internal/agent rather than internal/agent/cursor because
// internal/taskrun needs the removal half for §12.4 recovery — a daemon that
// crashed mid-step leaves the file behind — and internal/taskrun imports this
// package and not the adapter packages.
//
// Two consequences are handled rather than assumed away, and both are in §9.7:
// the file is untracked *inside a git worktree*, so it is visible to
// `git status`, to the task diff and to dirty detection while the step runs;
// and a daemon crash leaves it behind, which is what RemoveCursorMCPConfig is
// called from recovery for.

// CursorMCPDir and CursorMCPFile are the workspace config's path components,
// joined with filepath.Join so this is correct on Windows too.
const (
	CursorMCPDir  = ".cursor"
	CursorMCPFile = "mcp.json"
)

// CursorMCPConfigPath is the workspace MCP config inside a task worktree.
func CursorMCPConfigPath(workDir string) string {
	return filepath.Join(workDir, CursorMCPDir, CursorMCPFile)
}

// WriteCursorMCPConfig writes the workspace MCP config for one run.
func WriteCursorMCPConfig(workDir string, srv *MCPServer) error {
	body, err := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			srv.Name: map[string]any{
				"url":     srv.URL,
				"headers": map[string]string{"Authorization": "Bearer " + srv.Token},
			},
		},
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("render cursor mcp config: %w", err)
	}
	dir := filepath.Join(workDir, CursorMCPDir)
	// 0700, like the file: the directory exists only to hold a config that
	// carries the step's bearer token.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// 0600: the file carries the step's bearer token, and unlike claude's
	// argv it persists on disk for the life of the run.
	if err := os.WriteFile(CursorMCPConfigPath(workDir), body, 0o600); err != nil {
		return fmt.Errorf("write cursor mcp config: %w", err)
	}
	return nil
}

// RemoveCursorMCPConfig removes the workspace MCP config, and the `.cursor`
// directory when nothing else is in it. A file that is not there is not an
// error: this is called both at the end of a normal run and from §12.4
// recovery, and the common case in recovery is that there was never one.
func RemoveCursorMCPConfig(workDir string) error {
	if err := os.Remove(CursorMCPConfigPath(workDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove cursor mcp config: %w", err)
	}
	// os.Remove on a non-empty directory fails, which is exactly the check
	// wanted: a `.cursor` the user or the agent put something else in stays.
	_ = os.Remove(filepath.Join(workDir, CursorMCPDir))
	return nil
}
