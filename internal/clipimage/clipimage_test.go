package clipimage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var (
	pngHead  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpegHead = []byte("\xff\xd8\xff\xe0\x00\x10JFIF")
	gifHead  = []byte("GIF89a\x01\x00\x01\x00")
	webpHead = []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
)

// fakeHelpers replaces the exec and PATH hooks for one test. present names
// the helpers on PATH; run answers each run.
func fakeHelpers(t *testing.T, present []string, run func(limit int64, name string, args []string) ([]byte, error)) {
	t.Helper()
	oldRun, oldFind := runHelper, findHelper
	t.Cleanup(func() { runHelper, findHelper = oldRun, oldFind })
	findHelper = func(name string) bool {
		for _, p := range present {
			if p == name {
				return true
			}
		}
		return false
	}
	runHelper = func(_ context.Context, limit int64, name string, args ...string) ([]byte, error) {
		return run(limit, name, args)
	}
}

func TestSniff(t *testing.T) {
	for _, tc := range []struct {
		data []byte
		want string
	}{
		{pngHead, "png"},
		{jpegHead, "jpeg"},
		{gifHead, "gif"},
		{[]byte("GIF87a"), "gif"},
		{webpHead, "webp"},
		{[]byte("RIFF\x24\x00\x00\x00WAVE"), ""},
		{[]byte("hello world, not an image"), ""},
		{[]byte("BM\x00\x00"), ""},
		{nil, ""},
	} {
		if got := sniff(tc.data); got != tc.want {
			t.Errorf("sniff(%q) = %q, want %q", tc.data, got, tc.want)
		}
	}
}

func TestImagePath(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	png := write("shot.png", pngHead)
	spaced := write("My Shot (1).png", pngHead)
	jpeg := write("noext", jpegHead)
	gif := write("a.gif", gifHead)
	webp := write("a.webp", webpHead)
	text := write("notes.png", []byte("just some text here"))
	empty := write("empty.png", nil)
	if err := os.Mkdir(filepath.Join(dir, "dir.png"), 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, in, want string
	}{
		{"plain", png, png},
		{"trimmed", "  " + png + "\n", png},
		{"double quoted", `"` + spaced + `"`, spaced},
		{"single quoted", "'" + spaced + "'", spaced},
		{"spaces as is", spaced, spaced},
		{"jpeg without extension", jpeg, jpeg},
		{"gif", gif, gif},
		{"webp", webp, webp},
		{"text named png", text, ""},
		{"empty file", empty, ""},
		{"directory", filepath.Join(dir, "dir.png"), ""},
		{"missing", filepath.Join(dir, "missing.png"), ""},
		{"empty text", "   ", ""},
		{"two lines", png + "\n" + png, ""},
		{"path with prose", "see " + png, ""},
	}
	if runtime.GOOS != "windows" {
		escaped := filepath.Join(dir, `My\ Shot\ \(1\).png`)
		cases = append(cases, struct{ name, in, want string }{"backslash escaped", escaped, spaced})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ImagePath(tc.in)
			if ok != (tc.want != "") || got != tc.want {
				t.Fatalf("ImagePath(%q) = %q, %v; want %q", tc.in, got, ok, tc.want)
			}
		})
	}
}

func TestImagePathRelativeIsAbsolute(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "r.png"), pngHead, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	got, ok := ImagePath("r.png")
	if !ok || !filepath.IsAbs(got) || filepath.Base(got) != "r.png" {
		t.Fatalf("ImagePath(r.png) = %q, %v", got, ok)
	}
}

func TestReadNoHelper(t *testing.T) {
	fakeHelpers(t, nil, func(int64, string, []string) ([]byte, error) {
		t.Fatal("a helper ran though none is on PATH")
		return nil, nil
	})
	if _, err := Read(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("Read = %v, want ErrNoImage", err)
	}
}

func TestReadHelperErrorIsNoImage(t *testing.T) {
	fakeHelpers(t, allHelpers, func(int64, string, []string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	})
	if _, err := Read(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("Read = %v, want ErrNoImage", err)
	}
}

func TestReadTooLargeIsNoImage(t *testing.T) {
	fakeHelpers(t, allHelpers, func(int64, string, []string) ([]byte, error) {
		return nil, errTooLarge
	})
	if _, err := Read(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("Read = %v, want ErrNoImage", err)
	}
}

func TestExecHelperLimit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The test binary itself is the helper: -test.run with no match prints
	// "ok"-ish noise well past four bytes, and nothing about it is platform
	// specific.
	_, err = execHelper(context.Background(), 4, exe, "-test.run=^$", "-test.v")
	if !errors.Is(err, errTooLarge) {
		t.Fatalf("execHelper over the limit = %v, want errTooLarge", err)
	}
	out, err := execHelper(context.Background(), 1<<20, exe, "-test.run=^$")
	if err != nil || !bytes.Contains(out, []byte("PASS")) {
		t.Fatalf("execHelper = %q, %v", out, err)
	}
}

func TestExecHelperMissing(t *testing.T) {
	if _, err := execHelper(context.Background(), 10, filepath.Join(t.TempDir(), "no-such-helper")); err == nil {
		t.Fatal("execHelper ran a program that does not exist")
	}
}
