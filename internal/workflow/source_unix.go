//go:build unix

package workflow

import (
	"os"
	"syscall"
)

// sourceOpenFlags opens a workflow file for reading without following a final
// symlink (O_NOFOLLOW) and without blocking (O_NONBLOCK). Both matter because
// a scope directory holds whatever a registered repository put there: a
// symlink would promote a file from outside the scope into it, and an open of
// a FIFO with no writer never returns (§5.2, issue #136). O_NONBLOCK is a
// no-op on a regular file, so this reads normal workflows unchanged.
const sourceOpenFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
