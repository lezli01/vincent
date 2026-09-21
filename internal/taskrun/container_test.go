package taskrun

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/container"
	"github.com/lezli01/vincent/internal/store"
)

// fakeRuntime stands in for docker. No `go test` in this repository may need a
// container daemon (the testing conventions), so the seam is faked here and
// the real argv construction is pinned in internal/container's own table test.
type fakeRuntime struct {
	mu      sync.Mutex
	labels  map[string]string
	removed []string
	signals []string
	// consulted counts every call that would have shelled out to docker. It
	// is what proves the negative: an uncontainerized task must not reach the
	// runtime at all, and "removed nothing" alone would also be true of a
	// task that spawned `docker inspect` and found nothing.
	consulted int
	// execDirect, when set, answers ExecDirect — a test standing in for an
	// image's PATH and CLI. gateway is what Gateway reports.
	execDirect func(container.ExecSpec) []string
	gateway    string
	// execs is every ExecSpec Exec was asked to build, which is where the
	// pid-file key a launcher chose can be read (061 decision 9). How a Key
	// becomes argv is internal/container's own table test.
	execs []container.ExecSpec
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{labels: map[string]string{}} }

func (f *fakeRuntime) Name() string                              { return "fake" }
func (f *fakeRuntime) Available(context.Context) error           { return nil }
func (f *fakeRuntime) EnsureImage(context.Context, string) error { return nil }

func (f *fakeRuntime) Create(_ context.Context, spec container.CreateSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels[spec.Name] = spec.Labels[container.LabelTask]
	return spec.Name, nil
}

func (f *fakeRuntime) Exec(id string, spec container.ExecSpec) []string {
	f.mu.Lock()
	f.execs = append(f.execs, spec)
	f.mu.Unlock()
	return append([]string{"fake", "exec", id}, spec.Argv...)
}

// execSpecs is every ExecSpec Exec built, in order.
func (f *fakeRuntime) execSpecs() []container.ExecSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]container.ExecSpec(nil), f.execs...)
}

func (f *fakeRuntime) ExecDirect(id string, spec container.ExecSpec) []string {
	if f.execDirect != nil {
		return f.execDirect(spec)
	}
	return append([]string{"fake", "exec", id}, spec.Argv...)
}

func (f *fakeRuntime) Gateway(context.Context, string) (string, error) { return f.gateway, nil }

func (f *fakeRuntime) Signal(_ context.Context, id, key, signal string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, id+"/"+key+"/"+signal)
	return nil
}

func (f *fakeRuntime) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	delete(f.labels, id)
	return nil
}

func (f *fakeRuntime) Lookup(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consulted++
	if _, ok := f.labels[name]; !ok {
		return "", nil
	}
	return name, nil
}

func (f *fakeRuntime) TaskLabel(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.labels[id], nil
}

func (f *fakeRuntime) removals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

// signalled is every signal delivered, as "container/key/SIGNAL".
func (f *fakeRuntime) signalled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.signals...)
}

func (f *fakeRuntime) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.consulted
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestRecoveryRemovesTheTasksOwnContainer is decision 4's good case: a
// `running` step run carrying a container id whose container still claims the
// task is removed, which kills every process inside it.
func TestRecoveryRemovesTheTasksOwnContainer(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	rt := newFakeRuntime()
	name := container.Name(task.ID)
	if _, err := rt.Create(context.Background(), container.CreateSpec{
		Name:   name,
		Labels: map[string]string{container.LabelTask: strconv.FormatInt(task.ID, 10)},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	run := journalRun(t, st, task.ID, nil, nil)
	run.ContainerID = &name
	if err := st.UpdateStepRun(context.Background(), run); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}

	if _, err := Recover(context.Background(), st, discardLogger(),
		WithContainers(func(string) container.Runtime { return rt }, "fake")); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := rt.removals(); len(got) != 1 || got[0] != name {
		t.Errorf("removed = %v, want [%s]", got, name)
	}
}

// TestRecoveryLeavesAnotherTasksContainerAlone is §12.4's rule from the
// container side: what cannot be proved is not killed. A row naming a
// container whose label is a different task belongs to somebody else.
func TestRecoveryLeavesAnotherTasksContainerAlone(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	rt := newFakeRuntime()
	name := container.Name(task.ID)
	if _, err := rt.Create(context.Background(), container.CreateSpec{
		Name:   name,
		Labels: map[string]string{container.LabelTask: strconv.FormatInt(task.ID+999, 10)},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	run := journalRun(t, st, task.ID, nil, nil)
	run.ContainerID = &name
	if err := st.UpdateStepRun(context.Background(), run); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}

	if _, err := Recover(context.Background(), st, discardLogger(),
		WithContainers(func(string) container.Runtime { return rt }, "fake")); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := rt.removals(); len(got) != 0 {
		t.Errorf("removed somebody else's container: %v", got)
	}
}

// TestRecoveryWithoutAContainerIDTouchesNothing is the regression that matters
// most, at the recovery end: an installation that never set an image behaves
// exactly as it did before task 061.
func TestRecoveryWithoutAContainerIDTouchesNothing(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	rt := newFakeRuntime()
	journalRun(t, st, task.ID, nil, nil)

	if _, err := Recover(context.Background(), st, discardLogger(),
		WithContainers(func(string) container.Runtime { return rt }, "fake")); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := rt.removals(); len(got) != 0 {
		t.Errorf("a host step run reached the runtime: %v", got)
	}
}

// TestContainerIDSurvivesARoundTrip pins migration 0021's column: nil is the
// ordinary value and means the host, and a journaled id comes back verbatim.
func TestContainerIDSurvivesARoundTrip(t *testing.T) {
	st, projectID := recoverStore(t)
	task := recoverTask(t, st, projectID, store.TaskRunning)
	run := journalRun(t, st, task.ID, nil, nil)
	if got, err := st.GetStepRun(context.Background(), run.ID); err != nil {
		t.Fatalf("GetStepRun: %v", err)
	} else if got.ContainerID != nil {
		t.Errorf("a host step run journaled a container id: %v", *got.ContainerID)
	}
	id := "deadbeef"
	run.ContainerID = &id
	if err := st.UpdateStepRun(context.Background(), run); err != nil {
		t.Fatalf("UpdateStepRun: %v", err)
	}
	got, err := st.GetStepRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if got.ContainerID == nil || *got.ContainerID != id {
		t.Errorf("ContainerID = %v, want %q", got.ContainerID, id)
	}
}

// TestContainerMountsAreIdenticalInsideAndOut is decision 2. A worktree's
// `.git` is a file holding an absolute gitdir into the parent repository, so
// both paths have to be mounted, and both at their own path — mount either
// alone, or either elsewhere, and the repository does not resolve.
func TestContainerMountsAreIdenticalInsideAndOut(t *testing.T) {
	mounts := containerMounts("/repos/app", "/data/worktrees/7", config.Container{MountAgentConfig: false})
	want := map[string]bool{"/repos/app": true, "/data/worktrees/7": true}
	for _, m := range mounts {
		if m.Source != m.Target {
			t.Errorf("mount %q is not at its own path inside (%q)", m.Source, m.Target)
		}
		delete(want, m.Source)
	}
	if len(want) != 0 {
		t.Errorf("missing mounts: %v", want)
	}
}

// TestDefaultContainerMountsAgentConfigUnderTheVincentHome is task 062.2
// decision 3, measured where the mounts are built: a user who sets only
// `container.image` finds the host's agent configuration directories beneath
// the vincent home, where HOME points, and not at their own host paths. The
// worktree and repository keep theirs (061 decision 2).
func TestDefaultContainerMountsAgentConfigUnderTheVincentHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	want := map[string]string{}
	for _, dir := range []string{".claude", ".codex", ".cursor"} {
		p := filepath.Join(home, dir)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		want[p] = container.HomeDir + "/" + dir
	}
	c := config.Default().Container
	c.Image = "alpine:3"
	for _, m := range containerMounts("/repos/app", "/data/worktrees/7", c) {
		target, ok := want[m.Source]
		if !ok {
			continue
		}
		if m.Target != target || m.ReadOnly {
			t.Errorf("mount %q → %q (ro=%v), want read-write at %q", m.Source, m.Target, m.ReadOnly, target)
		}
		delete(want, m.Source)
	}
	if len(want) != 0 {
		t.Errorf("agent config not mounted: %v", want)
	}
	c.MountAgentConfig = false
	if got := agentConfigMounts(c); got != nil {
		t.Errorf("mounts with mount_agent_config off = %v, want none", got)
	}
}

// TestContainerEnvHome is decision 3's HOME rule: the vincent home while the
// mounts are on, the image's own HOME while they are off, and whatever the
// user's own environment policy says about HOME when it says anything.
func TestContainerEnvHome(t *testing.T) {
	on := config.Container{Image: "img", MountAgentConfig: true}
	off := config.Container{Image: "img"}
	cases := []struct {
		name string
		env  config.Environment
		c    config.Container
		want string // "" = no HOME at all
	}{
		{"mounts on", config.Environment{}, on, container.HomeDir},
		{"mounts off", config.Environment{}, off, ""},
		{"policy sets HOME", config.Environment{Set: map[string]string{"HOME": "/work"}}, on, "/work"},
		{"policy unsets HOME", config.Environment{Unset: []string{"HOME"}}, on, ""},
		{
			"policy inherits HOME by name",
			config.Environment{Inherit: config.Inherit{Mode: config.InheritListMode, Names: []string{"HOME"}}},
			on, "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Environment = tc.env
			r := New(Deps{Config: func() config.Config { return cfg }})
			got := ""
			for _, kv := range r.containerEnv(tc.c) {
				if v, ok := strings.CutPrefix(kv, "HOME="); ok {
					got = v
				}
			}
			if got != tc.want {
				t.Errorf("HOME = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExecKeyIsPerRun is what keeps a parallel group's sub-steps from sharing
// one pid file while exec'ing into the one container (§7.5).
func TestExecKeyIsPerRun(t *testing.T) {
	if execKey(1) == execKey(2) {
		t.Fatal("two step runs share a pid file")
	}
}

// hostSnapshot and imageSnapshot are the two workflow snapshots archiving has
// to tell apart. They carry a real step because Parse refuses an empty
// `steps:` — a snapshot that does not parse is a snapshot the fallback path
// handles, not this one.
const (
	hostSnapshot = "name: adhoc\nsteps:\n  - id: s\n    type: command\n    run: exit 0\n"

	imageSnapshot = "name: adhoc\ndefaults:\n  container:\n    image: alpine:3\n" +
		"steps:\n  - id: s\n    type: command\n    run: exit 0\n"
)

// containerRunner is the smallest Runner removeTaskContainer needs: the
// resolved config and the runtime factory, no store and no worktree manager.
func containerRunner(image string, rt container.Runtime) *Runner {
	cfg := config.Default()
	cfg.Container.Image = image
	return &Runner{deps: Deps{
		Config:     func() config.Config { return cfg },
		Containers: func(string) container.Runtime { return rt },
		Logger:     discardLogger(),
	}}
}

// TestArchiveWithoutAnImageNeverConsultsTheRuntime is the archive end of the
// `image: ""` promise, and the regression that produced it: removeTaskContainer
// used to look the container up unconditionally, so every archive of every
// task on any host with docker installed spawned `docker inspect`. That is not
// merely wasted work — on the Windows CI leg it took the archive past the API
// client's 10 s deadline and reddened a build that had nothing to do with
// containers.
func TestArchiveWithoutAnImageNeverConsultsTheRuntime(t *testing.T) {
	rt := newFakeRuntime()
	r := containerRunner("", rt)
	task := &store.Task{ID: 7, WorkflowSnapshot: hostSnapshot}

	r.removeTaskContainer(context.Background(), task, discardLogger())

	if got := rt.calls(); got != 0 {
		t.Errorf("an uncontainerized archive reached the runtime %d time(s)", got)
	}
}

// TestArchiveRemovesAContainerizedTasksContainer is the other half: the guard
// must not have turned the removal off for the tasks that do have one.
func TestArchiveRemovesAContainerizedTasksContainer(t *testing.T) {
	rt := newFakeRuntime()
	r := containerRunner("alpine:3", rt)
	task := &store.Task{ID: 7, WorkflowSnapshot: hostSnapshot}
	name := container.Name(task.ID)
	if _, err := rt.Create(context.Background(), container.CreateSpec{
		Name:   name,
		Labels: map[string]string{container.LabelTask: strconv.FormatInt(task.ID, 10)},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	r.removeTaskContainer(context.Background(), task, discardLogger())

	if got := rt.removals(); len(got) != 1 || got[0] != name {
		t.Errorf("removed = %v, want [%s]", got, name)
	}
}

// TestArchiveHonoursTheWorkflowsOwnImage pins where the verdict is read from.
// A workflow that names an image the daemon's own block does not is a
// containerized task, and the snapshot is the only record of that once the
// task is being archived.
func TestArchiveHonoursTheWorkflowsOwnImage(t *testing.T) {
	rt := newFakeRuntime()
	r := containerRunner("", rt)
	task := &store.Task{ID: 7, WorkflowSnapshot: imageSnapshot}
	name := container.Name(task.ID)
	if _, err := rt.Create(context.Background(), container.CreateSpec{
		Name:   name,
		Labels: map[string]string{container.LabelTask: strconv.FormatInt(task.ID, 10)},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	r.removeTaskContainer(context.Background(), task, discardLogger())

	if got := rt.removals(); len(got) != 1 || got[0] != name {
		t.Errorf("removed = %v, want [%s]", got, name)
	}
}

// TestChatSkillLauncherPlacesTheProbeWhereTheTurnRuns is task 124.17
// decision 1, which amends task 124 decision 56: GET /v1/chats/{id}/skills no
// longer reads a settings-only bit, it resolves the same placement the next
// turn would use. A host task costs no runtime call at all; a containerized
// one costs the lookup that finding its container needs.
func TestChatSkillLauncherPlacesTheProbeWhereTheTurnRuns(t *testing.T) {
	cases := []struct {
		name     string
		image    string // the daemon's own container.image
		snapshot string
		running  bool // whether the task's container exists
		wantHost bool
	}{
		{"host workflow", "", hostSnapshot, false, true},
		{"workflow names an image", "", imageSnapshot, true, false},
		{"daemon names an image", "alpine:3", hostSnapshot, true, false},
		{"a configured container that is gone", "alpine:3", hostSnapshot, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, projectID := recoverStore(t)
			task := &store.Task{
				ProjectID: projectID, Title: "linked", WorkflowName: "adhoc",
				WorkflowSnapshot: tc.snapshot, BaseBranch: "main", BranchName: "vincent/1-linked",
				State: store.TaskBlocked,
			}
			if err := st.CreateTask(context.Background(), task, nil); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			rt := newFakeRuntime()
			r := containerRunner(tc.image, rt)
			r.deps.Store = st
			if tc.running {
				if _, err := rt.Create(context.Background(), container.CreateSpec{
					Name:   container.Name(task.ID),
					Labels: map[string]string{container.LabelTask: strconv.FormatInt(task.ID, 10)},
				}); err != nil {
					t.Fatalf("Create: %v", err)
				}
			}

			l, place, env, err := r.ChatSkillLauncher(context.Background(), task.ID, 42)
			switch {
			case tc.wantHost:
				if err != nil || place != "" || env != nil {
					t.Fatalf("ChatSkillLauncher on a host task = %q, %v, %v; want the host",
						place, env, err)
				}
				if _, ok := l.(agent.HostLauncher); !ok {
					t.Errorf("launcher = %T, want agent.HostLauncher", l)
				}
				// The bit this replaced never asked the runtime, and neither
				// does a host task: only a container has to be found.
				if n := rt.calls(); n != 0 {
					t.Errorf("a host task reached the runtime %d time(s)", n)
				}
			case !tc.running:
				if !errors.Is(err, ErrTaskContainerMissing) {
					t.Fatalf("ChatSkillLauncher with no container = %v, want ErrTaskContainerMissing", err)
				}
				if l != nil {
					t.Errorf("launcher = %T, want nil", l)
				}
			default:
				if err != nil {
					t.Fatalf("ChatSkillLauncher: %v", err)
				}
				if want := container.Name(task.ID); place != want {
					t.Errorf("place = %q, want the container id %q", place, want)
				}
				// Decision 3's environment, the same containerEnv an agent
				// step gets: the CLI reads the configuration
				// `mount_agent_config` mounted rather than the image's HOME.
				if !slices.Contains(env, "HOME="+container.HomeDir) {
					t.Errorf("env = %v, want it to carry HOME=%s", env, container.HomeDir)
				}
			}
			if l == nil || tc.wantHost {
				return
			}
			// Decision 4's pid-file namespace, keyed by chat: a probe has no
			// turn, and `skills-` collides with neither `chat-` nor `step-`.
			_, _ = l.Launch(agent.Command{Path: "claude", Args: []string{"--help"}, Dir: "/w"})
			specs := rt.execSpecs()
			if len(specs) != 1 || specs[0].Key != "skills-42" {
				t.Errorf("exec specs = %+v, want one keyed skills-42", specs)
			}
		})
	}
}

// TestChatSkillLauncherPropagatesAMissingTask keeps a store error an error: a
// task that cannot be read is not a task that runs on the host.
func TestChatSkillLauncherPropagatesAMissingTask(t *testing.T) {
	st, _ := recoverStore(t)
	rt := newFakeRuntime()
	r := containerRunner("", rt)
	r.deps.Store = st

	if _, _, _, err := r.ChatSkillLauncher(context.Background(), 999, 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ChatSkillLauncher on a missing task = %v, want ErrNotFound", err)
	}
	if n := rt.calls(); n != 0 {
		t.Errorf("ChatSkillLauncher reached the runtime %d time(s)", n)
	}
}

// TestStopChatSkillProbeSignalsTheProbeKey is decision 4's recovery half: the
// kill reaches `skills-<chatID>` inside the task's container, never the
// turn's `chat-` file, and a task with no container reports false so the
// sweep costs nothing on a host board.
//
// It takes skillProbeGrace to run, and that is the point of skillProbeGrace:
// at containerGraceTimeout this test would sit for fifteen seconds, and so
// would every daemon start with one open linked chat on a containerized task.
func TestStopChatSkillProbeSignalsTheProbeKey(t *testing.T) {
	st, projectID := recoverStore(t)
	task := &store.Task{
		ProjectID: projectID, Title: "linked", WorkflowName: "adhoc",
		WorkflowSnapshot: hostSnapshot, BaseBranch: "main", BranchName: "vincent/1-linked",
		State: store.TaskBlocked,
	}
	if err := st.CreateTask(context.Background(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	rt := newFakeRuntime()
	hostRunner := containerRunner("", rt)
	hostRunner.deps.Store = st
	if hostRunner.StopChatSkillProbe(context.Background(), task.ID, 42) {
		t.Error("StopChatSkillProbe on a host task = true, want false")
	}
	if got := rt.signalled(); len(got) != 0 {
		t.Errorf("a host task signalled %v", got)
	}

	r := containerRunner("alpine:3", rt)
	r.deps.Store = st
	name := container.Name(task.ID)
	if _, err := rt.Create(context.Background(), container.CreateSpec{
		Name:   name,
		Labels: map[string]string{container.LabelTask: strconv.FormatInt(task.ID, 10)},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !r.StopChatSkillProbe(context.Background(), task.ID, 42) {
		t.Error("StopChatSkillProbe on a containerized task = false, want true")
	}
	want := []string{name + "/skills-42/TERM", name + "/skills-42/KILL"}
	if got := rt.signalled(); !slices.Equal(got, want) {
		t.Errorf("signals = %v, want %v", got, want)
	}
}
