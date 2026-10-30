package copilot

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestStartHandsTheLauncherItsSpawn pins what the launcher receives: the
// resolved binary, buildArgs' exact argv with the minted session id, the
// worktree as cwd, the environment with nil kept as nil and nothing added to
// it, and the prompt on stdin only — even one that reads like a flag.
func TestStartHandsTheLauncherItsSpawn(t *testing.T) {
	path := agenttest.BuildFakeAgent(t)
	env := append(os.Environ(), "FAKEAGENT_SCENARIO=success")
	tests := []struct {
		name string
		spec agent.RunSpec
	}{
		{
			name: "resolved environment, model and effort",
			spec: agent.RunSpec{Prompt: "launch seam prompt", Env: env, Model: "gpt-6-luna", Effort: "high"},
		},
		{
			name: "inherited environment",
			spec: agent.RunSpec{Prompt: "launch seam prompt"},
		},
		{
			// Decision 2: no prompt is ever looked for on argv, so these
			// stay prompts.
			name: "flag-shaped prompt",
			spec: agent.RunSpec{Prompt: "--resume", Env: env},
		},
		{
			name: "short flag-shaped prompt with a preamble",
			spec: agent.RunSpec{Prompt: "-v", Preamble: "context first", Env: env},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := tt.spec
			spec.PermissionMode = agent.FullAuto
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
				t.Fatalf("launcher saw %d commands, want 1", len(launches))
			}
			got := launches[0].Command
			wantArgs, err := buildArgs(spec, res.SessionID)
			if err != nil {
				t.Fatalf("buildArgs: %v", err)
			}
			if got.Path != path {
				t.Errorf("Path = %q, want the resolved binary %q", got.Path, path)
			}
			if !slices.Equal(got.Args, wantArgs) {
				t.Errorf("Args = %q, want buildArgs' %q", got.Args, wantArgs)
			}
			for _, flag := range []string{"--no-auto-update", "-p", "--prompt", "-C"} {
				if want := flag == "--no-auto-update"; slices.Contains(got.Args, flag) != want {
					t.Errorf("argv contains %q = %v, want %v", flag, !want, want)
				}
			}
			if f, ok := flagAfter(got.Args, "--output-format"); !ok || f != "json" {
				t.Errorf("--output-format = %q, want json", f)
			}
			for _, a := range got.Args {
				if a == spec.Prompt || strings.Contains(a, "context first") {
					t.Errorf("argv %q carries the prompt; it belongs on stdin only", got.Args)
				}
			}
			if got.Dir != spec.WorkDir {
				t.Errorf("Dir = %q, want %q", got.Dir, spec.WorkDir)
			}
			if (got.Env == nil) != (spec.Env == nil) || !slices.Equal(got.Env, spec.Env) {
				t.Errorf("Env nil = %v, want nil = %v (and equal)", got.Env == nil, spec.Env == nil)
			}
			if got.StdinPipe {
				t.Error("StdinPipe = true; copilot has no mid-run input")
			}
			if got.Stderr == nil {
				t.Error("Stderr is nil; the no-result failure would carry nothing")
			}
			if stdin := launches[0].Stdin(); string(stdin) != spec.JoinedPrompt() {
				t.Errorf("stdin = %q, want the joined prompt %q", stdin, spec.JoinedPrompt())
			}
			if want := append([]string{path}, wantArgs...); !slices.Equal(h.Argv(), want) {
				t.Errorf("Argv() = %q, want %q", h.Argv(), want)
			}
		})
	}
}

// TestNeverSetsCopilotAllowAll pins decision 4 on the child's environment:
// the env spelling of full-auto also trusts the worktree, so vincent never
// adds it — not to a resolved environment, and not to an inherited one.
func TestNeverSetsCopilotAllowAll(t *testing.T) {
	for _, env := range [][]string{nil, {"PATH=" + os.Getenv("PATH")}} {
		rec := &agenttest.RecordingLauncher{}
		h, err := fakeAdapter(t).Start(t.Context(), agent.RunSpec{
			Prompt: "hi", WorkDir: t.TempDir(), PermissionMode: agent.FullAuto, Env: env, Launcher: rec,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		drain(t, h)
		_, _ = h.Wait()
		for _, kv := range rec.Launches()[0].Command.Env {
			if strings.HasPrefix(kv, "COPILOT_ALLOW_ALL=") {
				t.Errorf("child env carries %q", kv)
			}
		}
		if !slices.Equal(rec.Launches()[0].Command.Env, env) {
			t.Errorf("Env = %q, want exactly %q", rec.Launches()[0].Command.Env, env)
		}
	}
}

// TestStripsCopilotAllowAll (review F2): a COPILOT_ALLOW_ALL already in the
// child's environment — resolved by §12.3 or inherited from the daemon — is
// removed in every permission mode. Left in, it would approve every tool past
// a restricted run's --available-tools and trust the worktree, loading hooks a
// restricted agent can write. Every other variable passes through in order.
func TestStripsCopilotAllowAll(t *testing.T) {
	for _, mode := range []agent.PermissionMode{agent.Restricted, agent.FullAuto} {
		for _, inherited := range []bool{false, true} {
			var env, want []string
			if inherited {
				t.Setenv("COPILOT_ALLOW_ALL", "true")
				t.Setenv("copilot_allow_all", "1")
			} else {
				env = []string{"A=1", "COPILOT_ALLOW_ALL=true", "B=2", "Copilot_Allow_All=yes"}
				want = []string{"A=1", "B=2"}
			}
			rec := &agenttest.RecordingLauncher{}
			h, err := fakeAdapter(t).Start(t.Context(), agent.RunSpec{
				Prompt: "hi", WorkDir: t.TempDir(), PermissionMode: mode, Env: env, Launcher: rec,
			})
			if err != nil {
				t.Fatalf("%s, inherited=%v: Start: %v", mode, inherited, err)
			}
			drain(t, h)
			_, _ = h.Wait()
			got := rec.Launches()[0].Command.Env
			if got == nil {
				t.Fatalf("%s, inherited=%v: Env = nil, which inherits COPILOT_ALLOW_ALL", mode, inherited)
			}
			for _, kv := range got {
				if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, "COPILOT_ALLOW_ALL") {
					t.Errorf("%s, inherited=%v: child env carries %q", mode, inherited, kv)
				}
			}
			if !inherited && !slices.Equal(got, want) {
				t.Errorf("%s: Env = %q, want %q", mode, got, want)
			}
			if inherited && !slices.Contains(got, "PATH="+os.Getenv("PATH")) {
				t.Errorf("%s: inherited Env lost PATH: %q", mode, got)
			}
			if env != nil && env[1] != "COPILOT_ALLOW_ALL=true" {
				t.Errorf("%s: Start mutated the caller's Env: %q", mode, env)
			}
		}
	}
}

// TestDetectProbePinsTheBuild: the version probe carries --no-auto-update
// too (decision 5), and nothing else but --version.
func TestDetectProbePinsTheBuild(t *testing.T) {
	path := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_DIALECT", "copilot")
	rec := &agenttest.RecordingLauncher{}
	av, err := New(func() string { return path }).detectWith(t.Context(), rec)
	if err != nil || !av.Found {
		t.Fatalf("detect: %+v, %v", av, err)
	}
	want := [][]string{{path, "--no-auto-update", "--version"}}
	if got := rec.Probes(); !reflect.DeepEqual(got, want) {
		t.Errorf("probes = %q, want %q", got, want)
	}
}

// TestNilLauncherIsTheHostLauncher pins RunSpec.Launcher's nil rule: leaving
// it unset and passing agent.HostLauncher{} are the same run, down to every
// stream line and the assembled result — the session id aside, which is
// minted per run by design.
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
			lines = append(lines, normalize(string(ev.Raw)))
		}
		res, err := h.Wait()
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		argv := slices.Clone(h.Argv())
		if i := slices.Index(argv, "--session-id"); i >= 0 {
			argv[i+1] = "<id>"
		}
		res.SessionID = ""
		return argv, lines, res
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

// normalize blanks what differs between two identical runs of the fake: its
// timestamps and the session id it echoes.
func normalize(line string) string {
	line = tsRe.ReplaceAllString(line, `"timestamp":"<ts>"`)
	return sidRe.ReplaceAllString(line, `"sessionId":"<id>"`)
}

var (
	tsRe  = regexp.MustCompile(`"timestamp":"[^"]*"`)
	sidRe = regexp.MustCompile(`"sessionId":"[^"]*"`)
)
