package taskrun

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"

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
	return append([]string{"fake", "exec", id}, spec.Argv...)
}

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

// TestExecKeyIsPerRun is what keeps a parallel group's sub-steps from sharing
// one pid file while exec'ing into the one container (§7.5).
func TestExecKeyIsPerRun(t *testing.T) {
	if execKey(1) == execKey(2) {
		t.Fatal("two step runs share a pid file")
	}
}
