//go:build !windows

package daemon

import "context"

// RunManaged is Run everywhere except Windows. launchd and systemd supervise
// an ordinary foreground process and signal it to stop, which Run already
// handles — only the Windows SCM needs a protocol of its own (spec §12.1).
func RunManaged(ctx context.Context, opts Options) error { return Run(ctx, opts) }
