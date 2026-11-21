package taskrun

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"testing"

	"github.com/lezli01/vincent/internal/container"
	"github.com/lezli01/vincent/internal/store"
)

// TestChatShellRunsWhereTheChatsTurnsRun pins a chat shell command's
// placement (task 170): the host's §8.3 shell in the workspace for a chat
// that is not linked and for one on a host task, the container's /bin/sh —
// under a pid-file key of its own — for a task whose workflow runs in one,
// and a refusal rather than a host run when that container is gone.
func TestChatShellRunsWhereTheChatsTurnsRun(t *testing.T) {
	cases := []struct {
		name      string
		linked    bool
		snapshot  string
		running   bool
		container bool
		missing   bool
	}{
		{name: "a chat that is not linked", snapshot: hostSnapshot},
		{name: "linked to a host task", linked: true, snapshot: hostSnapshot},
		{name: "linked to a containerized task", linked: true, snapshot: imageSnapshot, running: true, container: true},
		{name: "the container is gone", linked: true, snapshot: imageSnapshot, missing: true},
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
			r := containerRunner("", rt)
			r.deps.Store = st
			r.deps.Shells = NewShells(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if tc.running {
				if _, err := rt.Create(context.Background(), container.CreateSpec{
					Name:   container.Name(task.ID),
					Labels: map[string]string{container.LabelTask: strconv.FormatInt(task.ID, 10)},
				}); err != nil {
					t.Fatalf("Create: %v", err)
				}
			}
			var taskID int64
			if tc.linked {
				taskID = task.ID
			}
			in, err := r.ChatRunsInContainer(context.Background(), task.ID)
			if err != nil || in != (tc.container || tc.missing) {
				t.Errorf("ChatRunsInContainer = %v, %v", in, err)
			}
			work := t.TempDir()
			ex, err := r.ChatShell(context.Background(), taskID, 7, work, "git --version")
			if tc.missing {
				if !errors.Is(err, ErrTaskContainerMissing) {
					t.Fatalf("ChatShell = %v, want ErrTaskContainerMissing", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ChatShell: %v", err)
			}
			if !tc.container {
				if ex.Dir != work || ex.Stop != nil || len(rt.execSpecs()) != 0 {
					t.Errorf("host run = dir %q, stop set %v, %d exec(s)", ex.Dir, ex.Stop != nil, len(rt.execSpecs()))
				}
				if ex.Argv[len(ex.Argv)-1] != "git --version" {
					t.Errorf("host argv = %q, want the command last", ex.Argv)
				}
				return
			}
			specs := rt.execSpecs()
			if len(specs) != 1 {
				t.Fatalf("container run built %d exec(s), want 1", len(specs))
			}
			spec := specs[0]
			if spec.Key != "chatshell-7" || spec.WorkDir != work ||
				!slices.Equal(spec.Argv, []string{"/bin/sh", "-c", "git --version"}) {
				t.Errorf("exec spec = %+v", spec)
			}
			if ex.Dir != "" || ex.Env != nil || ex.Stop == nil {
				t.Errorf("container run = dir %q, env %v, stop set %v", ex.Dir, ex.Env, ex.Stop != nil)
			}
		})
	}
}
