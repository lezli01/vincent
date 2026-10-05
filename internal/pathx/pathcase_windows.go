//go:build windows

package pathx

// caseInsensitivePaths is true where the platform's filesystem compares paths
// without regard to case, so `C:\Repo` and `c:\repo` are one directory. It
// decides how SameDir compares a path git printed against one the user
// configured; it is a platform fact, not a filesystem probe, because the
// comparison has to work for a path that does not exist.
const caseInsensitivePaths = true
