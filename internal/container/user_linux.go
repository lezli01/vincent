package container

import (
	"os"
	"strconv"
)

// HostUser is the `--user` value every exec and the container itself run as
// (task 061 decision 5). On Linux that is the invoking user, so files land
// owned correctly in the mounted worktree and git inside the container sees
// the same owner as the worktree on disk. An image with a baked non-root user
// is overridden; an image whose HOME is writable only by its own user needs
// HOME pointed at a mounted directory, which the example Dockerfiles do.
func HostUser() string {
	return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
}
