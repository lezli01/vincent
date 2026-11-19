package nerdfont

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fixture serves a small archive holding two faces and an unrelated file.
func fixture(t *testing.T) (Release, map[string][]byte) {
	t.Helper()
	faces := map[string][]byte{"A.ttf": []byte("face a"), "B.ttf": []byte("face b, longer")}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"A.ttf", "B.ttf", "Other.ttf"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(faces[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(srv.Close)
	return Release{
		URL: srv.URL, Size: int64(len(body)), SHA256: hash(body),
		Faces: []Face{
			{"A.ttf", int64(len(faces["A.ttf"])), hash(faces["A.ttf"])},
			{"B.ttf", int64(len(faces["B.ttf"])), hash(faces["B.ttf"])},
		},
	}, faces
}

func state(t *testing.T, rel Release, dir string) State {
	t.Helper()
	s, err := Detect(rel, dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInstallWritesOnlyThePinnedFaces(t *testing.T) {
	rel, faces := fixture(t)
	dir := filepath.Join(t.TempDir(), "fonts")
	if got := state(t, rel, dir); got != StateAbsent {
		t.Fatalf("a missing directory is %s", got)
	}
	if err := Install(context.Background(), rel, dir); err != nil {
		t.Fatal(err)
	}
	if got := state(t, rel, dir); got != StateInstalled {
		t.Fatalf("after installing the state is %s", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(faces) {
		t.Fatalf("the directory holds %d files, want %d", len(entries), len(faces))
	}
}

func TestInstallRefusesAnArchiveThatDoesNotMatch(t *testing.T) {
	rel, _ := fixture(t)
	rel.SHA256 = hash([]byte("something else"))
	dir := t.TempDir()
	if err := Install(context.Background(), rel, dir); err == nil {
		t.Fatal("a tampered archive was installed")
	}
	if got := state(t, rel, dir); got != StateAbsent {
		t.Fatalf("a refused archive left the directory %s", got)
	}
}

func TestInstallRefusesAFaceThatDoesNotMatch(t *testing.T) {
	rel, _ := fixture(t)
	rel.Faces[1].SHA256 = hash([]byte("something else"))
	dir := t.TempDir()
	if err := Install(context.Background(), rel, dir); err == nil {
		t.Fatal("a face with the wrong content was installed")
	}
	if _, err := os.Stat(filepath.Join(dir, "B.ttf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the bad face is on disk: %v", err)
	}
}

// Someone else's file of the same name stops the install and survives removal.
func TestAnotherFontOfTheSameNameIsLeftAlone(t *testing.T) {
	rel, _ := fixture(t)
	dir := t.TempDir()
	theirs := filepath.Join(dir, "A.ttf")
	if err := os.WriteFile(theirs, []byte("their font"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := state(t, rel, dir); got != StateConflict {
		t.Fatalf("the state is %s", got)
	}
	if err := Install(context.Background(), rel, dir); err == nil {
		t.Fatal("the install went ahead over another font")
	}
	removed, err := Uninstall(context.Background(), rel, dir)
	if err != nil || removed != 0 {
		t.Fatalf("uninstall removed %d: %v", removed, err)
	}
	if got, _ := os.ReadFile(theirs); string(got) != "their font" {
		t.Fatalf("their file now reads %q", got)
	}
}

func TestUninstallRemovesWhatWasInstalled(t *testing.T) {
	rel, faces := fixture(t)
	dir := t.TempDir()
	if err := Install(context.Background(), rel, dir); err != nil {
		t.Fatal(err)
	}
	removed, err := Uninstall(context.Background(), rel, dir)
	if err != nil || removed != len(faces) {
		t.Fatalf("uninstall removed %d: %v", removed, err)
	}
	if got := state(t, rel, dir); got != StateAbsent {
		t.Fatalf("after removing the state is %s", got)
	}
}

func TestDir(t *testing.T) {
	cases := []struct{ goos, xdg, want string }{
		{"darwin", "", filepath.Join("/h", "Library", "Fonts")},
		{"linux", "", filepath.Join("/h", ".local", "share", "fonts")},
		{"linux", "/x", filepath.Join("/x", "fonts")},
		{"linux", "relative", filepath.Join("/h", ".local", "share", "fonts")},
	}
	for _, c := range cases {
		if got, err := Dir(c.goos, "/h", c.xdg); err != nil || got != c.want {
			t.Errorf("Dir(%s, %q) = %q, %v, want %q", c.goos, c.xdg, got, err, c.want)
		}
	}
	if _, err := Dir("windows", "/h", ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("windows answered %v", err)
	}
}

func TestTerminal(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    string
		bundled bool
		remote  bool
	}{
		{"ghostty", map[string]string{"TERM_PROGRAM": "ghostty"}, "Ghostty", true, false},
		{"wezterm", map[string]string{"TERM_PROGRAM": "WezTerm"}, "WezTerm", true, false},
		{"iterm", map[string]string{"TERM_PROGRAM": "iTerm.app"}, "iTerm2", false, false},
		{"kitty", map[string]string{"TERM": "xterm-kitty"}, "kitty", false, false},
		{"unknown over ssh", map[string]string{"SSH_CONNECTION": "1 2 3 4"}, "", false, true},
	}
	for _, c := range cases {
		got := Terminal(func(k string) string { return c.env[k] })
		if got.Name != c.want || got.Bundled != c.bundled || got.Remote != c.remote {
			t.Errorf("%s: %+v", c.name, got)
		}
		if !got.Bundled && got.Steps == "" {
			t.Errorf("%s: no steps", c.name)
		}
	}
}
