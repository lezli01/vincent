package pathx

import (
	"os"
	"path/filepath"
	"strings"
)

// CaseInsensitive reports whether this platform's paths compare without
// regard to case — a platform fact, not a filesystem probe.
func CaseInsensitive() bool { return caseInsensitivePaths }

// SameDir reports whether two paths name the same directory.
//
// Both sides are run through filepath.Clean and, where the platform's paths
// are case-insensitive, folded: git prints paths in its own form —
// `/private/var/...` on macOS, forward slashes on Windows — and the
// comparison has to work for a path that does not exist, so the lexical
// answer comes first. Then the symlink-resolved forms are compared, which
// catches macOS's /var → /private/var without making the lexical case depend
// on the filesystem. Last, os.SameFile, for the two directories no spelling
// reconciles — a hard-linked or bind-mounted path, a Windows short name.
func SameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if equal(a, b) {
		return true
	}
	ra, erra := filepath.EvalSymlinks(a)
	rb, errb := filepath.EvalSymlinks(b)
	if erra != nil || errb != nil {
		return false
	}
	if equal(ra, rb) {
		return true
	}
	fa, err := os.Stat(ra)
	if err != nil {
		return false
	}
	fb, err := os.Stat(rb)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// Contains reports whether child is dir or lies beneath it. Containment is
// by path component, never by string prefix — `/a/web` does not contain
// `/a/webapp` — and is tried lexically first and then in symlink-resolved
// form, with SameDir's case folding.
func Contains(dir, child string) bool {
	dir, child = filepath.Clean(dir), filepath.Clean(child)
	if within(dir, child) {
		return true
	}
	rd, errd := filepath.EvalSymlinks(dir)
	rc, errc := filepath.EvalSymlinks(child)
	if errd != nil || errc != nil {
		return false
	}
	return within(rd, rc)
}

func equal(a, b string) bool {
	if caseInsensitivePaths {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// within is Contains' lexical half over two cleaned paths.
func within(dir, child string) bool {
	if caseInsensitivePaths {
		dir, child = strings.ToLower(dir), strings.ToLower(child)
	}
	rel, err := filepath.Rel(dir, child)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
