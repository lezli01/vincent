//go:build darwin

package worktree

// caseInsensitivePaths: APFS and HFS+ are case-insensitive by default. A
// case-sensitive volume is possible, and folding there only ever makes
// sameDir answer true for two paths that differ in case alone — which on such
// a volume would be two directories nobody has, since both sides come from
// the same configured project path.
const caseInsensitivePaths = true
