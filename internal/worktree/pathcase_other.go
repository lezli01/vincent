//go:build !windows && !darwin

package worktree

// caseInsensitivePaths: Linux and the other unixes compare paths byte for
// byte, so `Repo` and `repo` are two directories.
const caseInsensitivePaths = false
