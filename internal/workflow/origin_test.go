package workflow

import (
	"path/filepath"
	"strings"
	"testing"
)

// Origin is what tells two definitions of the same name apart (task 043). The
// shadowing contract itself is unchanged — §5.2's built-in precedence stands —
// so these assert that the substitution is *visible*, not prevented.

func TestOriginTellsAShadowedAdhocFromTheBuiltin(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	repo := t.TempDir()
	writeWorkflow(t, filepath.Join(repo, ProjectDirName), AdhocName,
		manualWorkflow(AdhocName, "project override"))
	reg.ReloadGlobal()
	reg.SetProjects(map[int64]string{7: repo})

	shadowed, ok := reg.Lookup(7, AdhocName)
	if !ok {
		t.Fatal("the project's adhoc.yaml did not resolve")
	}
	builtin, ok := reg.Lookup(0, AdhocName)
	if !ok {
		t.Fatal("the built-in adhoc did not resolve")
	}

	got := shadowed.Origin(repo, globalDir)
	if got.Scope != ScopeProject {
		t.Errorf("shadowing entry scope = %q, want %q", got.Scope, ScopeProject)
	}
	if want := ".vincent/workflows/adhoc.yaml"; got.File != want {
		t.Errorf("shadowing entry file = %q, want %q", got.File, want)
	}
	base := builtin.Origin(repo, globalDir)
	if base.Scope != ScopeBuiltin {
		t.Errorf("built-in scope = %q, want %q", base.Scope, ScopeBuiltin)
	}
	if base.File != "" {
		t.Errorf("built-in file = %q, want none: a built-in has no file", base.File)
	}
	// The point of the whole feature: same name, different definition, and the
	// two are distinguishable on the task row afterwards.
	if got.Digest == base.Digest {
		t.Errorf("both digests are %q; a shadowed adhoc must not look like the built-in", got.Digest)
	}
	for _, d := range []string{got.Digest, base.Digest} {
		if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
			t.Errorf("digest = %q, want sha256:<64 hex>", d)
		}
	}
}

// TestOriginGlobalFileIsNamedFromTheConfigDir: a global entry reads
// `workflows/x.yaml`, which names the scope it came from — not the bare file
// name, and not the absolute path of whichever machine created the task.
func TestOriginGlobalFileIsNamedFromTheConfigDir(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	writeWorkflow(t, globalDir, "release", manualWorkflow("release", "global"))
	reg.ReloadGlobal()

	e, ok := reg.Lookup(0, "release")
	if !ok {
		t.Fatal("the global release.yaml did not resolve")
	}
	got := e.Origin("", reg.GlobalDir())
	if got.Scope != ScopeGlobal {
		t.Errorf("scope = %q, want %q", got.Scope, ScopeGlobal)
	}
	if want := "workflows/release.yaml"; got.File != want {
		t.Errorf("file = %q, want %q", got.File, want)
	}
	if filepath.IsAbs(got.File) {
		t.Errorf("file = %q, want a scope-relative path", got.File)
	}
}

// TestOriginDigestIsStableAcrossAReload: nothing about re-reading unchanged
// bytes may move the digest, or a task created before a reload would look like
// it came from a different file than one created after it.
func TestOriginDigestIsStableAcrossAReload(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	repo := t.TempDir()
	writeWorkflow(t, filepath.Join(repo, ProjectDirName), "feature",
		manualWorkflow("feature", "project"))
	reg.ReloadGlobal()
	reg.SetProjects(map[int64]string{7: repo})

	first, ok := reg.Lookup(7, "feature")
	if !ok {
		t.Fatal("feature did not resolve")
	}
	reg.Reload()
	second, ok := reg.Lookup(7, "feature")
	if !ok {
		t.Fatal("feature did not resolve after a reload")
	}
	a, b := first.Origin(repo, globalDir), second.Origin(repo, globalDir)
	if a != b {
		t.Errorf("origin changed across a reload of unchanged bytes:\n %+v\nthen\n %+v", a, b)
	}
}

// TestOriginDigestFollowsTheBytes: a rewritten file is a different definition,
// and the digest is the only field that says so — the scope and the path are
// unchanged.
func TestOriginDigestFollowsTheBytes(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	repo := t.TempDir()
	dir := filepath.Join(repo, ProjectDirName)
	writeWorkflow(t, dir, "feature", manualWorkflow("feature", "before"))
	reg.ReloadGlobal()
	reg.SetProjects(map[int64]string{7: repo})
	before, _ := reg.Lookup(7, "feature")

	writeWorkflow(t, dir, "feature", manualWorkflow("feature", "after"))
	reg.Reload()
	after, _ := reg.Lookup(7, "feature")

	a, b := before.Origin(repo, globalDir), after.Origin(repo, globalDir)
	if a.File != b.File || a.Scope != b.Scope {
		t.Fatalf("scope/file moved unexpectedly: %+v then %+v", a, b)
	}
	if a.Digest == b.Digest {
		t.Errorf("digest = %q for both revisions of the file", a.Digest)
	}
}

// TestSourceDigestHashesRawBytes: no normalization. A CRLF checkout genuinely
// is different bytes on disk, and inventing a canonical form here would make
// the digest claim two files agree when the daemon parsed different sources.
func TestSourceDigestHashesRawBytes(t *testing.T) {
	lf := "name: x\nsteps: []\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	if SourceDigest(lf) == SourceDigest(crlf) {
		t.Error("LF and CRLF sources digest the same; the digest is not over raw bytes")
	}
	if got, want := SourceDigest(lf), SourceDigest("name: x\nsteps: []\n"); got != want {
		t.Errorf("SourceDigest is not deterministic: %q then %q", want, got)
	}
}
