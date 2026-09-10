package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The version-drift guard (task 095 decision 6).
//
// `metadata.version` in a SKILL.md is what every "installed and current" row
// in vincent rests on, and it is the one field nothing else forces anybody to
// touch: `references/*.md` goes stale on its own, and a hand bump is
// forgotten. So the content of each published skill is hashed here, and this
// test fails when the tree moved and the version did not.
//
// The version lives inside SKILL.md, so bumping it changes the hash too:
// there is no way to satisfy this test by editing one of the two. When it
// fails, bump `metadata.version` and paste the hash the failure prints.
var publishedTrees = map[string]string{
	"vincent-workflows": "f8e21bdd936d676d58a2ab3775898fe8b5560ad2c0974f5dbe527780a348d387",
}

func TestPublishedSkillsAreVersioned(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		seen[name] = true
		want, ok := publishedTrees[name]
		if !ok {
			t.Errorf("skills/%s/ is published but has no row in publishedTrees; "+
				"add one with the hash below so its version cannot go stale unnoticed:\n\t%q: %q,",
				name, name, hashTree(t, name))
			continue
		}
		if got := hashTree(t, name); got != want {
			t.Errorf("skills/%s/ changed but its recorded hash did not.\n"+
				"Bump metadata.version in skills/%s/SKILL.md, then set:\n\t%q: %q,",
				name, name, name, got)
		}
	}
	for name := range publishedTrees {
		if !seen[name] {
			t.Errorf("publishedTrees names %q, which is no longer under skills/", name)
		}
	}
}

// hashTree digests every file under one skill, path and content. Line endings
// are normalized and paths are slash-separated so the digest is the same on
// all three platforms — a guard that fires only on Windows is a guard nobody
// can act on.
func hashTree(t *testing.T, dir string) string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		h.Write([]byte(filepath.ToSlash(p)))
		h.Write([]byte{0})
		h.Write([]byte(strings.ReplaceAll(string(b), "\r\n", "\n")))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
