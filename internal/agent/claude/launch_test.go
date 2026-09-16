package claude

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestStartHandsTheLauncherItsSpawn pins task 062.1's no-behaviour-change
// claim for claude: the Command the launcher receives is exactly what Start
// used to exec itself — the resolved binary, buildArgs' argv, the worktree,
// the environment with nil kept as nil, and the prompt on stdin in whichever
// shape the input gate chose.
func TestStartHandsTheLauncherItsSpawn(t *testing.T) {
	path := agenttest.BuildFakeAgent(t)
	env := append(os.Environ(), "FAKEAGENT_SCENARIO=success")
	tests := []struct {
		name string
		// version is what the --version probe reports; "" keeps fakeagent's
		// default, which passes the §7.4 input gate.
		version   string
		inputMode bool
		spec      agent.RunSpec
	}{
		{
			name:      "input mode with a resolved environment",
			inputMode: true,
			spec:      agent.RunSpec{PermissionMode: agent.FullAuto, Env: env},
		},
		{
			name:      "input mode inheriting the environment",
			inputMode: true,
			spec: agent.RunSpec{
				PermissionMode: agent.Restricted, Model: "opus", Effort: "max",
				MCP: &agent.MCPServer{Name: "vincent", URL: "http://127.0.0.1:1/mcp", Token: "tok"},
			},
		},
		{
			name:    "plain mode below the input gate",
			version: "1.0.0",
			spec:    agent.RunSpec{PermissionMode: agent.FullAuto, Env: env},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.version != "" {
				// The probe inherits the process environment, never
				// RunSpec.Env, so the version is set where it will look.
				t.Setenv("FAKEAGENT_VERSION", tt.version)
			}
			spec := tt.spec
			spec.Prompt = "launch seam prompt"
			spec.WorkDir = t.TempDir()
			rec := &agenttest.RecordingLauncher{}
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
				t.Fatalf("launcher saw %d commands, want 1 (probes do not go through it)", len(launches))
			}
			got := launches[0].Command
			wantArgs, err := buildArgs(spec, tt.inputMode)
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
			if got.Stderr == nil {
				t.Error("Stderr is nil; the failure tail would be lost")
			}
			if got.StdinPipe != tt.inputMode {
				t.Errorf("StdinPipe = %v, want %v", got.StdinPipe, tt.inputMode)
			}
			wantStdin := []byte(spec.Prompt)
			if tt.inputMode {
				if wantStdin, err = userMessageLine(spec.Prompt); err != nil {
					t.Fatalf("userMessageLine: %v", err)
				}
			}
			// In input mode the line is written after launch and may be
			// followed by nothing else here: success asks no question.
			if stdin := launches[0].Stdin(); string(stdin) != string(wantStdin) {
				t.Errorf("stdin = %q, want %q", stdin, wantStdin)
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
