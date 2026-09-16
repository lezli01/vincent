package cursor

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestStartHandsTheLauncherItsSpawn pins task 062.1's no-behaviour-change
// claim for cursor: the Command the launcher receives is exactly what Start
// used to exec itself — the resolved binary, buildArgs' argv, the worktree,
// the environment with nil kept as nil, the prompt on stdin — and the
// workspace `.cursor/mcp.json` is already on disk when the launch happens,
// because cursor reads it at startup.
func TestStartHandsTheLauncherItsSpawn(t *testing.T) {
	path := agenttest.BuildFakeAgent(t)
	env := append(os.Environ(), "FAKEAGENT_SCENARIO=success")
	tests := []struct {
		name string
		spec agent.RunSpec
	}{
		{
			name: "resolved environment",
			spec: agent.RunSpec{PermissionMode: agent.FullAuto, Env: env, Model: "gpt-5"},
		},
		{
			name: "inherited environment with an MCP server",
			spec: agent.RunSpec{
				PermissionMode: agent.FullAuto,
				MCP:            &agent.MCPServer{Name: "vincent", URL: "http://127.0.0.1:1/mcp", Token: "tok"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := tt.spec
			spec.Prompt = "launch seam prompt"
			spec.WorkDir = t.TempDir()
			configAtLaunch := errors.New("OnLaunch never ran")
			rec := &agenttest.RecordingLauncher{OnLaunch: func(cmd agent.Command) {
				_, configAtLaunch = os.Stat(agent.CursorMCPConfigPath(cmd.Dir))
			}}
			spec.Launcher = rec

			h, err := New(func() string { return path }).Start(t.Context(), spec)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			drain(t, h)
			res, err := h.Wait()
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if res.IsError || res.ExitCode != 0 {
				t.Fatalf("run failed: %+v", res)
			}

			launches := rec.Launches()
			if len(launches) != 1 {
				t.Fatalf("launcher saw %d commands, want 1", len(launches))
			}
			got := launches[0].Command
			wantArgs, err := buildArgs(spec)
			if err != nil {
				t.Fatalf("buildArgs: %v", err)
			}
			if got.Path != path {
				t.Errorf("Path = %q, want the resolved binary %q", got.Path, path)
			}
			if !slices.Equal(got.Args, wantArgs) {
				t.Errorf("Args = %q, want buildArgs' %q", got.Args, wantArgs)
			}
			if got.Dir != spec.WorkDir {
				t.Errorf("Dir = %q, want %q", got.Dir, spec.WorkDir)
			}
			if (got.Env == nil) != (spec.Env == nil) || !slices.Equal(got.Env, spec.Env) {
				t.Errorf("Env nil = %v, want nil = %v (and equal)", got.Env == nil, spec.Env == nil)
			}
			if got.StdinPipe {
				t.Error("StdinPipe = true; cursor-agent -p has no mid-run input")
			}
			if got.Stderr == nil {
				t.Error("Stderr is nil; the no-result failure would carry nothing")
			}
			if stdin := launches[0].Stdin(); string(stdin) != spec.Prompt {
				t.Errorf("stdin = %q, want the prompt %q", stdin, spec.Prompt)
			}
			if spec.MCP != nil && configAtLaunch != nil {
				t.Errorf("mcp.json at launch: %v, want it written before the process starts", configAtLaunch)
			}
			if want := append([]string{path}, wantArgs...); !slices.Equal(h.Argv(), want) {
				t.Errorf("Argv() = %q, want %q", h.Argv(), want)
			}
		})
	}
}

// TestNilLauncherIsTheHostLauncher pins RunSpec.Launcher's nil rule: leaving
// it unset and passing agent.HostLauncher{} are the same run, down to the
// argv, every stream line and the assembled result.
func TestNilLauncherIsTheHostLauncher(t *testing.T) {
	path := agenttest.BuildFakeAgent(t)
	workDir := t.TempDir()
	run := func(l agent.Launcher) ([]string, []string, agent.RunResult) {
		t.Helper()
		h, err := New(func() string { return path }).Start(t.Context(), agent.RunSpec{
			Prompt:         "same run twice",
			WorkDir:        workDir,
			PermissionMode: agent.FullAuto,
			Env:            append(os.Environ(), "FAKEAGENT_SCENARIO=success"),
			Launcher:       l,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		var lines []string
		for _, ev := range drain(t, h) {
			lines = append(lines, string(ev.Raw))
		}
		res, err := h.Wait()
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		return h.Argv(), lines, res
	}
	nilArgv, nilLines, nilRes := run(nil)
	hostArgv, hostLines, hostRes := run(agent.HostLauncher{})
	if !slices.Equal(nilArgv, hostArgv) {
		t.Errorf("Argv: nil launcher %q, host launcher %q", nilArgv, hostArgv)
	}
	if !slices.Equal(nilLines, hostLines) {
		t.Errorf("transcript lines differ:\nnil:  %q\nhost: %q", nilLines, hostLines)
	}
	if !reflect.DeepEqual(nilRes, hostRes) {
		t.Errorf("RunResult: nil launcher %+v, host launcher %+v", nilRes, hostRes)
	}
}
