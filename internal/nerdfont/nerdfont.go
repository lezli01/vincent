package nerdfont

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Family is the name a terminal's font setting lists. The Mono variant keeps
// every icon to one cell, which is what the TUI's layouts measure.
const Family = "JetBrainsMono Nerd Font Mono"

// Face is one font file, as pinned.
type Face struct {
	Name   string
	Size   int64
	SHA256 string
}

// Release is an archive and the faces taken from it.
type Release struct {
	Version string
	URL     string
	Size    int64
	SHA256  string
	Faces   []Face
}

// Pinned is the release vincent installs. Moving it is a code change, so what
// a given vincent downloads is fixed when it is built.
var Pinned = Release{
	Version: "v3.5.1",
	URL:     "https://github.com/ryanoasis/nerd-fonts/releases/download/v3.5.1/JetBrainsMono.zip",
	Size:    133975870,
	SHA256:  "fab782a66f7d3019da64f6572db9fc5d3a4bcb19f9fa13e2d8a62e3693d6396e",
	Faces: []Face{
		{"JetBrainsMonoNerdFontMono-Regular.ttf", 2573248, "f2a5ea6cfab397445ffab00c0370927b66d61e560a05db5db271b42006381c1a"},
		{"JetBrainsMonoNerdFontMono-Bold.ttf", 2577016, "bfcf9a917276ffc058867d87cbc8a5b2f1ab0f4b710e9170dc02763ccb80bd4b"},
		{"JetBrainsMonoNerdFontMono-Italic.ttf", 2575368, "31efd6ead98746f5b0afa1ee6dba60267ad48db36428360bee327bec10621f97"},
		{"JetBrainsMonoNerdFontMono-BoldItalic.ttf", 2578280, "9dba502e00e35209f6ed2a151c7376c051657b067cdebbc6e52d06cb9002cf31"},
	},
}

// ErrUnsupported is an operating system vincent cannot install fonts on yet.
var ErrUnsupported = errors.New("installing the font is supported on macOS and Linux only")

// State is what a font directory holds of a release.
type State string

// The states of a font directory.
const (
	// StateAbsent is none of the faces.
	StateAbsent State = "absent"
	// StatePartial is some of the faces, and nothing in the way of the rest.
	StatePartial State = "partial"
	// StateInstalled is every face.
	StateInstalled State = "installed"
	// StateConflict is a file with a face's name and other content.
	StateConflict State = "conflict"
)

// Dir is the per-user font directory for an operating system.
func Dir(goos, home, xdgDataHome string) (string, error) {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Fonts"), nil
	case "linux":
		if filepath.IsAbs(xdgDataHome) {
			return filepath.Join(xdgDataHome, "fonts"), nil
		}
		return filepath.Join(home, ".local", "share", "fonts"), nil
	}
	return "", ErrUnsupported
}

// DefaultDir is this machine's per-user font directory.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return Dir(runtime.GOOS, home, os.Getenv("XDG_DATA_HOME"))
}

// Detect reads what dir holds of rel.
func Detect(rel Release, dir string) (State, error) {
	found := 0
	for _, f := range rel.Faces {
		ours, exists, err := holds(filepath.Join(dir, f.Name), f)
		if err != nil {
			return "", err
		}
		if exists && !ours {
			return StateConflict, nil
		}
		if ours {
			found++
		}
	}
	switch found {
	case 0:
		return StateAbsent, nil
	case len(rel.Faces):
		return StateInstalled, nil
	}
	return StatePartial, nil
}

// holds reports whether path exists and whether it is exactly face.
func holds(path string, face Face) (ours, exists bool, err error) {
	file, err := os.Open(path) //nolint:gosec // G304: a pinned face's name in the user's font directory
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	defer func() { _ = file.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return false, true, err
	}
	return hex.EncodeToString(sum.Sum(nil)) == face.SHA256, true, nil
}

// Install downloads rel, verifies it, and writes its missing faces into dir. A
// file of the same name with other content stops it before anything is fetched.
func Install(ctx context.Context, rel Release, dir string) error {
	state, err := Detect(rel, dir)
	if err != nil {
		return err
	}
	switch state {
	case StateInstalled:
		return nil
	case StateConflict:
		return fmt.Errorf("%s already holds a different font file with the same name; nothing was changed", dir)
	}
	archive, err := download(ctx, rel)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(archive) }()
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // a font directory is world-readable by design
		return err
	}
	if err := extract(archive, rel, dir); err != nil {
		return err
	}
	return refreshCache(ctx, dir)
}

// download fetches the archive to a temporary file and proves it is the pinned
// one. Nothing reads its contents until it has.
func download(ctx context.Context, rel Release) (path string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download the font: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download the font: the server answered %s", resp.Status)
	}
	file, err := os.CreateTemp("", "vincent-nerdfont-*.zip")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	sum := sha256.New()
	// One byte past the pinned size is enough to know it is the wrong file.
	n, err := io.Copy(io.MultiWriter(file, sum), io.LimitReader(resp.Body, rel.Size+1))
	if err != nil {
		return "", fmt.Errorf("download the font: %w", err)
	}
	if n != rel.Size || hex.EncodeToString(sum.Sum(nil)) != rel.SHA256 {
		return "", errors.New("the downloaded font does not match the pinned checksum; nothing was installed")
	}
	return file.Name(), nil
}

// extract writes every face dir does not hold yet.
func extract(archive string, rel Release, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	for _, face := range rel.Faces {
		if ours, _, err := holds(filepath.Join(dir, face.Name), face); err != nil {
			return err
		} else if ours {
			continue
		}
		entry, err := zr.Open(face.Name)
		if err != nil {
			return fmt.Errorf("the font archive has no %s: %w", face.Name, err)
		}
		err = writeFace(entry, face, dir)
		_ = entry.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// writeFace verifies a face while writing it beside its destination, and only
// then moves it into place, so a font directory never holds half a font.
func writeFace(src io.Reader, face Face, dir string) (err error) {
	tmp, err := os.CreateTemp(dir, ".vincent-font-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(src, face.Size+1))
	if err != nil {
		return err
	}
	if n != face.Size || hex.EncodeToString(sum.Sum(nil)) != face.SHA256 {
		return fmt.Errorf("%s does not match its pinned checksum", face.Name)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, face.Name))
}

// Uninstall removes the faces vincent would have written and reports how many.
// A file of the same name with other content is not vincent's and stays.
func Uninstall(ctx context.Context, rel Release, dir string) (int, error) {
	removed := 0
	for _, face := range rel.Faces {
		path := filepath.Join(dir, face.Name)
		ours, _, err := holds(path, face)
		if err != nil {
			return removed, err
		}
		if !ours {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, err
		}
		removed++
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, refreshCache(ctx, dir)
}

// refreshCache tells fontconfig about the change. macOS watches its font folders
// and a Linux without fontconfig has no cache, so a missing tool is not a failure.
func refreshCache(ctx context.Context, dir string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	tool, err := exec.LookPath("fc-cache")
	if err != nil {
		return nil //nolint:nilerr // no fontconfig means no cache to refresh
	}
	//nolint:gosec // G204: the program is the constant fc-cache resolved on PATH
	if out, err := exec.CommandContext(ctx, tool, "-f", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("fc-cache: %w: %s", err, out)
	}
	return nil
}
