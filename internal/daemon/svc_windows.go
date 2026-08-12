//go:build windows

package daemon

import (
	"context"

	"golang.org/x/sys/windows/svc"
)

// RunManaged runs the daemon, using the Windows Service Control Manager
// protocol when the process was started by the SCM (spec §12.1).
//
// This branch is why `sc.exe create` pointed at a plain `vincent daemon`
// would not have worked: the SCM expects a control-code handshake within
// about 30 seconds and kills anything that stays silent with error 1053
// ("the service did not respond to the start or control request in a timely
// fashion"). Nothing about shutdown is reimplemented here — the Stop handler
// cancels the very context Run already drains.
//
// `vincent service install` no longer registers a service (T4.17: it runs as
// LocalSystem, which is the wrong user); it registers a Scheduled Task, whose
// parent is the scheduler's svchost rather than services.exe, so
// IsWindowsService reports false and this falls through to Run. The branch
// stays because it is what makes a hand-rolled `sc.exe create` work at all,
// and because a daemon that cannot tell it is under the SCM would be killed
// by it rather than diagnosed.
func RunManaged(ctx context.Context, opts Options) error {
	inService, err := svc.IsWindowsService()
	if err != nil || !inService {
		// Not under the SCM (an interactive `vincent daemon`, or the check
		// itself failed): behave exactly as before rather than refusing to
		// start over a diagnostic.
		return Run(ctx, opts)
	}
	return svc.Run(serviceName, &windowsService{ctx: ctx, opts: opts})
}

// serviceName must match internal/service.Label; the SCM passes it back as
// argv[0] of Execute.
const serviceName = "vincent"

type windowsService struct {
	ctx  context.Context
	opts Options
}

// Execute implements svc.Handler.
func (w *windowsService) Execute(
	_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status,
) (svcSpecificEC bool, exitCode uint32) {
	// Accept stop and shutdown only. Pause/continue would have to suspend the
	// scheduler and every running step, which §6 has no state for.
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	s <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx, w.opts) }()

	s <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case err := <-done:
			// The daemon exited on its own — a fatal startup error, or a stop
			// requested through the API. Report a nonzero code for the former
			// so the SCM's recovery settings can see it.
			if err != nil {
				return false, 1
			}
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				// Wait for the real shutdown: §12.4 gives running steps a
				// grace period, and reporting Stopped before that finishes
				// would let the SCM kill the process mid-teardown.
				err := <-done
				if err != nil {
					return false, 1
				}
				return false, 0
			default:
				// Unexpected control codes are ignored rather than fatal.
				s <- c.CurrentStatus
			}
		}
	}
}
