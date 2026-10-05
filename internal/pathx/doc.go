// Package pathx compares directory paths the way vincent means "the same
// directory" and "inside this directory": lexically after filepath.Clean,
// case-folded where the platform's paths are case-insensitive, and then again
// in their symlink-resolved forms.
//
// It is a leaf with no internal imports. It exists because three places
// needed the one comparison and two of them had grown their own: the
// worktree package asks whether a path git printed is the project's own
// checkout, the API asks whether a project being registered is one that
// already is, and the TUI asks which registered project or worktree contains
// the directory it was launched from (task 132.3, spec §15). The TUI must
// not import internal/worktree, a package that runs git, to get it.
package pathx
