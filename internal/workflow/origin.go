package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// Origin is where one registry entry's definition came from (§5.2, task 043):
// the scope that won the shadowing walk, the source file **relative to that
// scope's root**, and a digest of the source bytes as loaded.
//
// It exists because `workflow_name` alone cannot tell two definitions apart. A
// project or global file named `adhoc` shadows the built-in — a phase 2
// decision that stands (§5.2) — so a task recording only the name leaves the
// substitution invisible forever afterwards. This is how it is seen.
//
// The path is scope-relative rather than absolute: a task row outlives the
// checkout it was created in, and `/Users/someone/src/app/.vincent/...` is
// machine state, not provenance. It is spelled with forward slashes on every
// platform for the same reason — the same committed file must read the same in
// a task created on Windows and one created on Linux.
type Origin struct {
	Scope Scope `json:"scope"`
	// File is empty for ScopeBuiltin: a built-in has no file.
	File string `json:"file,omitempty"`
	// Digest is `sha256:<hex>` over Source, hashed exactly as loaded.
	Digest string `json:"digest,omitempty"`
}

// Origin describes where this entry came from. projectRoot is the repo root of
// the entry's project scope and globalDir is `{config_dir}/workflows`; either
// may be empty, in which case a path under it degrades to its base name rather
// than leaking an absolute one.
//
// It lives here because this package is the only one that holds both halves:
// the absolute path the loader recorded and the scope root it was found under.
func (e Entry) Origin(projectRoot, globalDir string) Origin {
	o := Origin{Scope: e.Scope, Digest: SourceDigest(e.Source)}
	switch e.Scope {
	case ScopeBuiltin:
		// No file, by construction: builtins() sets Source and leaves File
		// empty. The digest still identifies which binary's copy ran.
	case ScopeProject:
		o.File = scopeRelative(e.File, projectRoot, "")
	case ScopeGlobal:
		// Relative to the *config directory*, not to globalDir itself, so the
		// value reads `workflows/x.yaml` and names the scope it came from.
		o.File = scopeRelative(e.File, globalDir, GlobalDirName)
	}
	return o
}

// SourceDigest hashes a workflow source as `sha256:<hex>`.
//
// Raw bytes, with no normalization: this codebase has no canonical form for a
// workflow file, and a CRLF checkout genuinely is different bytes on disk. A
// normalizer here would make the digest claim two files agree when the daemon
// would parse them from different sources. The consequence — the same committed
// file digesting differently on a Windows checkout with `autocrlf` — is stated
// in docs/tasks/043-workflow-origin.md rather than papered over.
func SourceDigest(src string) string {
	sum := sha256.Sum256([]byte(src))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// scopeRelative renders path relative to root, under an optional prefix. A path
// that is not under root — or a root nobody supplied — degrades to the base
// name: a relative path is the point, so an absolute fallback would defeat it.
func scopeRelative(path, root, prefix string) string {
	if path == "" {
		return ""
	}
	rel := ""
	if root != "" {
		if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}
	if rel == "" {
		rel = filepath.Base(path)
	}
	if prefix != "" {
		rel = filepath.Join(prefix, rel)
	}
	return filepath.ToSlash(rel)
}

// GlobalDir is `{config_dir}/workflows`, the root of the global scope. It is
// exposed so a caller building an Origin can name that scope's root without
// having to re-derive the config directory the registry was constructed with.
func (r *Registry) GlobalDir() string { return r.globalDir }
