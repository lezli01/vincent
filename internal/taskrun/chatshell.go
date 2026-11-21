package taskrun

// A chat's shell passthrough (task 170, spec §5.5, §8.3, §8.5).
//
// A human runs a command in a chat's workspace through the daemon. Where and
// under which shell it runs is this package's answer for the same reason a
// linked turn's launcher is (linkedchat.go): the §8.3 resolver, the §8.5
// environment and the task's container all live here, and internal/chatrun
// never imports this package — the daemon injects ChatShell into it.

import (
	"context"
	"strconv"

	"github.com/lezli01/vincent/internal/container"
)

// ChatShellExec is one chat shell command resolved to a process to spawn.
type ChatShellExec struct {
	// Argv is the host command line: the resolved shell and the command on
	// the host, the runtime client's exec for a task's container.
	Argv []string
	// Dir and Env are the host process's. Both are empty inside a container,
	// where the directory and the environment ride in Argv and the runtime
	// client keeps the daemon's own environment to find its socket.
	Dir string
	Env []string
	// Stop signals the command inside the container, TERM then KILL through
	// its pid file; nil on the host, where killing the process tree is the
	// whole stop. Killing only the runtime client leaves the command running
	// in the container (task 061 decision 9).
	Stop func()
}

// HostCommand is the argv that runs script under the platform default shell
// (§8.3): /bin/sh -c on POSIX, pwsh on Windows.
func (s *Shells) HostCommand(script string) ([]string, error) {
	sh, err := s.Default()
	if err != nil {
		return nil, err
	}
	return sh.command(script), nil
}

// ChatShell resolves where a chat's shell command runs (task 170): where the
// chat's turns run. taskID is the task a linked chat is on, 0 for every
// other chat, which runs on the host. A task whose workflow runs in a
// container gets the container's /bin/sh — the same inversion of §8.3 a
// containerized `run:` body accepts (task 061 decision 8) — and a configured
// container that is gone is ErrTaskContainerMissing rather than a host run.
//
// The environment is §8.5's base with no VINCENT_* block, because that block
// describes a task's step and this is neither: the §12.3 policy over the
// daemon's environment on the host, the image's plus the vincent home in a
// container. It carries no chat marker either — the command is the human's
// own, not an agent's.
func (r *Runner) ChatShell(
	ctx context.Context, taskID, runID int64, workDir, script string,
) (ChatShellExec, error) {
	var tc taskContainer
	if taskID != 0 {
		var err error
		if tc, err = r.chatContainer(ctx, taskID); err != nil {
			return ChatShellExec{}, err
		}
	}
	if !tc.active() {
		argv, err := r.deps.Shells.HostCommand(script)
		if err != nil {
			return ChatShellExec{}, err
		}
		return ChatShellExec{
			Argv: argv, Dir: workDir, Env: r.deps.Config().Environment.ResolveProcess(),
		}, nil
	}
	key := chatShellExecKey(runID)
	return ChatShellExec{
		Argv: tc.rt.Exec(tc.id, container.ExecSpec{
			Key: key, Argv: []string{"/bin/sh", "-c", script}, Env: r.containerEnv(tc.settings),
			WorkDir: workDir, User: container.HostUser(),
		}),
		Stop: func() { stopInContainer(tc, key, chatShellGrace, r.deps.Logger) },
	}, nil
}

// StopChatShellOrphan is §12.4's container-aware kill for a chat shell
// command a previous daemon died under, mirroring StopChatOrphan. It reports
// whether there was a container to signal into.
func (r *Runner) StopChatShellOrphan(ctx context.Context, taskID, runID int64) bool {
	tc, err := r.chatContainer(ctx, taskID)
	if err != nil || !tc.active() {
		return false
	}
	stopInContainer(tc, chatShellExecKey(runID), chatShellGrace, r.deps.Logger)
	return true
}

// ChatRunsInContainer reports whether a linked chat's turns on taskID run in
// a container, from the task's settings alone — no runtime lookup, so it
// answers the same whether or not the container is up. It is what refuses an
// image for such a chat (task 170): the stored file lives in the daemon's
// data directory, which is not mounted into the container.
func (r *Runner) ChatRunsInContainer(ctx context.Context, taskID int64) (bool, error) {
	c, err := r.chatContainerSettings(ctx, taskID)
	if err != nil {
		return false, err
	}
	return c.Enabled(), nil
}

// chatShellGrace is what a chat shell command gets between TERM and KILL
// inside a container. It is the skill probe's short grace rather than a
// step's 15 s: a human pressed stop on their own command and is waiting for
// the prompt back.
const chatShellGrace = skillProbeGrace

// chatShellExecKey names a chat shell run's pid file; its prefix collides
// with no step's, turn's or probe's.
func chatShellExecKey(runID int64) string { return "chatshell-" + strconv.FormatInt(runID, 10) }
