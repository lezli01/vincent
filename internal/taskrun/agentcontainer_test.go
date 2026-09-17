package taskrun

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/container"
	"github.com/lezli01/vincent/internal/store"
)

// TestHelperImage is not a test. It is the stand-in for a container image's
// shell: imageRuntime hands the step engine an argv that re-executes this test
// binary, and the arguments after "--" say what the image answers. Invoked
// normally there is no "--", and it returns at once.
func TestHelperImage(*testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 {
		return
	}
	args := os.Args[i+1:]
	switch args[0] {
	case "resolve": // resolve <path or ""> — `command -v` finding it, or not
		if args[1] == "" {
			os.Exit(1)
		}
		fmt.Println(args[1])
	case "version": // version <string> — the image CLI's --version
		fmt.Println(args[1])
	}
	os.Exit(0)
}

// imageRuntime is fakeRuntime answering like an image with an agent CLI in it:
// `command -v` finds imagePath (or nothing until a test sets it), `--version` reports
// imageVersion (or the host binary's own when it is ""), and an exec runs its
// argv on the host. Every exec spec is recorded.
type imageRuntime struct {
	*fakeRuntime
	imagePath    string
	imageVersion string

	mu     sync.Mutex
	execs  []container.ExecSpec
	direct []container.ExecSpec
}

func newImageRuntime(imageVersion string) *imageRuntime {
	rt := &imageRuntime{fakeRuntime: newFakeRuntime(), imageVersion: imageVersion}
	rt.gateway = "172.17.0.1"
	rt.execDirect = func(spec container.ExecSpec) []string {
		rt.mu.Lock()
		rt.direct = append(rt.direct, spec)
		rt.mu.Unlock()
		helper := []string{os.Args[0], "-test.run=^TestHelperImage$", "--"}
		switch {
		case len(spec.Argv) == 5 && spec.Argv[2] == `command -v "$1"`:
			return append(helper, "resolve", rt.imagePath)
		case slices.Contains(spec.Argv, "--version") && rt.imageVersion != "":
			return append(helper, "version", rt.imageVersion)
		}
		return spec.Argv
	}
	return rt
}

func (r *imageRuntime) Exec(_ string, spec container.ExecSpec) []string {
	r.mu.Lock()
	r.execs = append(r.execs, spec)
	r.mu.Unlock()
	return spec.Argv
}

func (r *imageRuntime) execSpecs() []container.ExecSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]container.ExecSpec(nil), r.execs...)
}

func (r *imageRuntime) directSpecs() []container.ExecSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]container.ExecSpec(nil), r.direct...)
}

const containerAgentWorkflow = `name: contained
steps:
  - id: build
    type: agent
    agent: claude
    max_retries: 0
    prompt: do the thing
`

// containerHarness is an engine harness whose tasks run in imageRuntime's
// "container", with agents.claude.path pointing at a host binary that must
// not be used, and the per-step MCP route recorded.
func containerHarness(t *testing.T, rt *imageRuntime, routes *[]MCPRoute) *engineHarness {
	t.Helper()
	var mu sync.Mutex
	return newEngineHarnessWith(t, func(c *config.Config) {
		c.Container.Image = "img"
	}, func(d *Deps) {
		d.Containers = func(string) container.Runtime { return rt }
		d.MCPForStep = func(_, _ int64, _ string, route MCPRoute) (*agent.MCPServer, func()) {
			mu.Lock()
			*routes = append(*routes, route)
			mu.Unlock()
			return nil, nil
		}
	})
}

// TestContainerizedAgentStepRunsThroughTheContainer is task 062.2's core: an
// agent step of a containerized task is resolved on the image's PATH by its
// bare binary name — agents.claude.path is a host path and is ignored — runs
// through the runtime's pid-file exec, journals the container id for §12.4,
// and is routed to the MCP endpoint as a containerized step.
func TestContainerizedAgentStepRunsThroughTheContainer(t *testing.T) {
	var routes []MCPRoute
	rt := newImageRuntime("")
	h := containerHarness(t, rt, &routes)
	// The image's claude is the fakeagent the harness configured for the host,
	// found by `command -v` rather than by agents.claude.path.
	fake, err := h.runner.deps.Agents.Get("claude")
	if !err {
		t.Fatal("claude not registered")
	}
	rt.imagePath, _ = fake.Path()
	task := h.createTask(t, containerAgentWorkflow)
	h.start(t)

	final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if final.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", final.State, final.BlockReason)
	}
	direct := rt.directSpecs()
	if len(direct) == 0 || direct[0].Argv[4] != "claude" {
		t.Fatalf("resolve execs = %+v, want `command -v claude` first", direct)
	}
	execs := rt.execSpecs()
	if len(execs) != 1 {
		t.Fatalf("execs = %d, want the one agent run", len(execs))
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 1 {
		t.Fatalf("step runs = %d, want 1", len(runs))
	}
	if want := execKey(runs[0].ID); execs[0].Key != want {
		t.Errorf("exec key = %q, want %q", execs[0].Key, want)
	}
	if execs[0].Argv[0] != rt.imagePath || execs[0].WorkDir != final.WorktreePath {
		t.Errorf("exec = %+v, want the image-resolved binary in the worktree", execs[0])
	}
	if !slices.Contains(execs[0].Env, "HOME="+container.HomeDir) {
		t.Errorf("exec env = %q, want the vincent home passed literally", execs[0].Env)
	}
	for _, e := range execs[0].Env {
		if strings.HasPrefix(e, "VINCENT_") && strings.Contains(e, "=") {
			t.Errorf("exec env carries %q on the argv, want the name only", e)
		}
	}
	if runs[0].ContainerID == nil || *runs[0].ContainerID != container.Name(task.ID) {
		t.Errorf("container id = %v, want %s journaled", runs[0].ContainerID, container.Name(task.ID))
	}
	if len(routes) != 1 || !routes[0].Container || routes[0].Gateway() != "172.17.0.1" {
		t.Errorf("mcp routes = %+v, want one containerized route with the gateway", routes)
	}
}

// TestContainerizedAgentMissingFromTheImage: a CLI the image lacks fails the
// step agent_unavailable, exactly as a missing host CLI does — even though the
// host has one.
func TestContainerizedAgentMissingFromTheImage(t *testing.T) {
	var routes []MCPRoute
	rt := newImageRuntime("")
	h := containerHarness(t, rt, &routes)
	task := h.createTask(t, containerAgentWorkflow)
	h.start(t)

	final := h.waitForState(t, task.ID, store.TaskBlocked, store.TaskDone)
	if final.State != store.TaskBlocked || final.BlockReason != ReasonAgentUnavailable {
		t.Fatalf("task = %s (%s), want blocked %s", final.State, final.BlockReason, ReasonAgentUnavailable)
	}
	if n := len(rt.execSpecs()); n != 0 {
		t.Errorf("execs = %d, want nothing launched", n)
	}
}

// TestContainerizedClaudeInputModeFollowsTheImage: §7.4's input mode is gated
// on the CLI version, and a containerized run's is the image's. The host
// fakeagent reports an input-capable version; the image reports one that is
// not, so the run must start without the control-protocol flags.
func TestContainerizedClaudeInputModeFollowsTheImage(t *testing.T) {
	var routes []MCPRoute
	rt := newImageRuntime("1.0.0 (Claude Code)")
	h := containerHarness(t, rt, &routes)
	fake, _ := h.runner.deps.Agents.Get("claude")
	rt.imagePath, _ = fake.Path()
	task := h.createTask(t, containerAgentWorkflow)
	h.start(t)
	h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)

	execs := rt.execSpecs()
	if len(execs) != 1 {
		t.Fatalf("execs = %d, want 1", len(execs))
	}
	if slices.Contains(execs[0].Argv, "--input-format") {
		t.Errorf("argv = %q, want plain mode: the image's claude is too old for input", execs[0].Argv)
	}
}

// TestUncontainerizedAgentStepUsesTheHostLauncher is the regression that
// matters most: with no image, the launcher is HostLauncher itself and no
// runtime is consulted.
func TestUncontainerizedAgentStepUsesTheHostLauncher(t *testing.T) {
	if l := agentLauncher(taskContainer{}, 1, discardLogger()); l != (agent.HostLauncher{}) {
		t.Fatalf("launcher = %#v, want agent.HostLauncher{}", l)
	}
	if route := mcpRouteFor(taskContainer{}, discardLogger()); route.Container || route.Gateway != nil {
		t.Errorf("route = %+v, want the host's zero route", route)
	}
	rt := newFakeRuntime()
	h := newEngineHarnessWith(t, nil, func(d *Deps) {
		d.Containers = func(string) container.Runtime { return rt }
	})
	task := h.createTask(t, containerAgentWorkflow)
	h.start(t)
	if final := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked); final.State != store.TaskDone {
		t.Fatalf("task = %s, want done", final.State)
	}
	if rt.calls() != 0 {
		t.Errorf("runtime consulted %d times for a task with no image", rt.calls())
	}
	if runs := h.stepRuns(t, task.ID); runs[0].ContainerID != nil {
		t.Errorf("container id = %q journaled for a host run", *runs[0].ContainerID)
	}
}

// TestContainerProcessStopsInsideTheContainer is 061 decision 9 for an agent
// run: Terminate and Kill reach the pid file's process in the container, the
// container survives (no Remove), and a signal vincent sent reads as the host's
// -1 rather than docker exec's 128+n.
func TestContainerProcessStopsInsideTheContainer(t *testing.T) {
	rt := newImageRuntime("")
	rt.labels["cid"] = "1"
	tc := taskContainer{id: "cid", rt: rt, settings: config.Container{Image: "img"}}
	l := agentLauncher(tc, 7, discardLogger())
	host, _ := os.Executable()
	p, err := l.Launch(agent.Command{Path: host, Args: []string{"-test.run=^TestHelperImage$", "--", "version", "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Terminate(); err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = p.Kill() // idempotent
	_, _ = p.Wait()
	p.Release()
	want := []string{"cid/step-7/TERM", "cid/step-7/KILL"}
	rt.mu.Lock()
	got := append([]string(nil), rt.signals...)
	rt.mu.Unlock()
	if !slices.Equal(got, want) {
		t.Errorf("signals = %q, want %q", got, want)
	}
	if len(rt.removals()) != 0 {
		t.Errorf("container removed on a step stop: %v", rt.removals())
	}
	for _, tc := range []struct {
		code     int
		signaled bool
		want     int
	}{{137, true, -1}, {143, true, -1}, {137, false, 137}, {1, true, 1}, {0, false, 0}} {
		if got := containerExitCode(tc.code, tc.signaled); got != tc.want {
			t.Errorf("containerExitCode(%d, %v) = %d, want %d", tc.code, tc.signaled, got, tc.want)
		}
	}
}

// TestContainerLauncherKeepsCodexTokenOffTheArgv is task 057 decision 8 in a
// container: codex's MCP token rides the environment so it is on no argv, and
// the host `docker exec` argv must not put it back on one.
func TestContainerLauncherKeepsCodexTokenOffTheArgv(t *testing.T) {
	rt := newImageRuntime("")
	tc := taskContainer{id: "cid", rt: rt, settings: config.Container{Image: "img"}}
	rt.imagePath = agenttest.BuildFakeAgent(t)
	var argvs [][]string
	l := &recordingArgv{Launcher: agentLauncher(tc, 3, discardLogger()), argvs: &argvs}
	handle, err := codex.New(func() string { return "" }).Start(t.Context(), agent.RunSpec{
		Prompt: "p", WorkDir: t.TempDir(), Env: []string{"A=1"},
		MCP:      &agent.MCPServer{Name: "vincent", URL: "http://host.docker.internal:1/mcp/step/3", Token: "s3cret-token"},
		Launcher: l,
	})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range handle.Events() {
		_ = ev // drained so Wait can return
	}
	_, _ = handle.Wait()
	argvs = append(argvs, handle.Argv())
	for _, spec := range append(rt.execSpecs(), rt.directSpecs()...) {
		argvs = append(argvs, spec.Env, spec.Argv)
	}
	for _, argv := range argvs {
		if strings.Contains(strings.Join(argv, " "), "s3cret-token") {
			t.Fatalf("token on an argv: %q", argv)
		}
	}
	if specs := rt.execSpecs(); len(specs) != 1 || !slices.Contains(specs[0].Env, codex.MCPTokenEnv) {
		t.Errorf("exec specs = %+v, want %s passed by name", specs, codex.MCPTokenEnv)
	}
}

// recordingArgv records each launched process's host argv.
type recordingArgv struct {
	agent.Launcher
	argvs *[][]string
}

func (r *recordingArgv) Launch(cmd agent.Command) (agent.Process, error) {
	p, err := r.Launcher.Launch(cmd)
	if err == nil {
		*r.argvs = append(*r.argvs, p.Argv())
	}
	return p, err
}
