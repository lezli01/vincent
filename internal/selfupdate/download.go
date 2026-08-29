package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DownloadBase is where release assets live. Tests point Options.DownloadBase
// at an httptest server serving a fake release built in a temp dir.
const DownloadBase = "https://github.com/lezli01/vincent/releases/download"

// DownloadTimeout bounds fetching one asset. An archive is a few megabytes;
// this is generous for a slow link and still finite on a black hole.
const DownloadTimeout = 5 * time.Minute

// maxAsset caps what is read off the wire and out of an archive. A release
// archive is single-digit megabytes, so this is two orders of magnitude of
// headroom and still refuses a decompression bomb (gosec G110).
const maxAsset = 256 << 20

// AssetName is the release archive for a GOOS/GOARCH pair, matching
// `.goreleaser.yaml`'s `name_template`. The version carries no "v": goreleaser
// interpolates `{{ .Version }}`, which is the tag with the prefix stripped.
func AssetName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("vincent_%s_%s_%s.%s", strings.TrimPrefix(version, "v"), goos, goarch, ext)
}

// BinaryName is what the archive calls the executable.
func BinaryName(goos string) string {
	if goos == "windows" {
		return "vincent.exe"
	}
	return "vincent"
}

// fetch downloads one release asset into memory.
//
// In memory rather than to disk because both of these are small and both are
// verified before anything touches the filesystem: writing an unverified
// archive next to the binary it might replace is exactly the window the
// verification exists to close.
func (u *Updater) fetch(ctx context.Context, tag, name string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s/%s", strings.TrimSuffix(u.downloadBase, "/"), tag, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", name, err)
	}
	resp, err := u.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", name, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAsset))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return body, nil
}

// verifyChecksum matches data's SHA-256 against the entry for name in a
// `checksums.txt` whose own signature has already been checked (or explicitly
// skipped). The order matters and is the whole design: this file is only
// trustworthy because step 1 said so.
func verifyChecksum(checksums []byte, name string, data []byte) error {
	want, ok := checksumFor(checksums, name)
	if !ok {
		// A missing entry is a refusal, never a pass. The `.pkg` is
		// deliberately outside checksums.txt (task 039), which is exactly why
		// "not listed" must not mean "nothing to check".
		return fmt.Errorf("checksums.txt has no entry for %s", name)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return nil
}

// checksumFor reads one `sha256sum`-format line: the digest, two spaces, the
// file name. goreleaser writes the base name only, but a leading "*" (binary
// mode) and a directory prefix are both cheap to tolerate.
func checksumFor(checksums []byte, name string) (string, bool) {
	for line := range strings.Lines(string(checksums)) {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		got := strings.TrimPrefix(fields[1], "*")
		if path.Base(filepath.ToSlash(got)) == name {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

// extractBinary pulls the vincent executable out of a verified archive.
//
// It reads the archive from memory and returns the binary in memory for the
// same reason fetch does: the bytes are trusted, but only as an archive, and
// the first thing to touch disk should be the file the swap renames into
// place.
func extractBinary(archive []byte, goos string) ([]byte, error) {
	want := BinaryName(goos)
	if goos == "windows" {
		return extractZip(archive, want)
	}
	return extractTarGz(archive, want)
}

func extractTarGz(archive []byte, want string) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != want {
			continue
		}
		// G110: bounded by maxAsset, which is two orders of magnitude above
		// a real release archive.
		data, err := io.ReadAll(io.LimitReader(tr, maxAsset))
		if err != nil {
			return nil, fmt.Errorf("read %s from archive: %w", want, err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("archive contains no %s", want)
}

func extractZip(archive []byte, want string) ([]byte, error) {
	zr, err := zip.NewReader(strings.NewReader(string(archive)), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	for _, f := range zr.File {
		if path.Base(f.Name) != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s in archive: %w", want, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxAsset))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s from archive: %w", want, err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("archive contains no %s", want)
}

// hostAsset names the archive for the machine this is running on.
func hostAsset(version string) string {
	return AssetName(version, runtime.GOOS, runtime.GOARCH)
}

// currentMode reads the mode bits of the file being replaced, so the
// replacement keeps them. A binary that comes back 0644 is a binary that no
// longer runs, and the user finds out at the worst moment.
func currentMode(path string) os.FileMode {
	const fallback = os.FileMode(0o755)
	info, err := os.Stat(path)
	if err != nil {
		return fallback
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		return fallback
	}
	return mode
}
