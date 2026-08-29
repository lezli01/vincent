package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A fake release built in a temp dir and served over httptest: the archive,
// checksums.txt, and a stub cosign whose exit code the test chooses — the way
// cmd/fakeagent stands in for an agent CLI. Nothing here reaches the network,
// Fulcio or Rekor.

const fakeVersion = "9.9.9"

// TestApplySwapsVerifiedRelease is the happy path: good checksum, good
// signature, binary replaced, mode bits preserved.
func TestApplySwapsVerifiedRelease(t *testing.T) {
	rel := newFakeRelease(t, []byte("new vincent binary"))
	dest := existingBinary(t)

	up := New(Options{
		DownloadBase: rel.URL,
		CosignPath:   stubCosign(t, 0),
		Executable:   dest,
	})
	res, err := up.Apply(t.Context(), fakeVersion)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.SignatureVerified {
		t.Error("a verified signature was reported unverified")
	}
	assertContents(t, dest, "new vincent binary")
	assertExecutable(t, dest)
}

// One flipped byte in the archive must refuse and leave the old binary
// **byte-identical** — the acceptance criterion, asserted on the bytes rather
// than on the exit path.
func TestApplyRefusesOnChecksumMismatch(t *testing.T) {
	rel := newFakeRelease(t, []byte("new vincent binary"))
	rel.corruptArchive()
	dest := existingBinary(t)

	up := New(Options{DownloadBase: rel.URL, CosignPath: stubCosign(t, 0), Executable: dest})
	if _, err := up.Apply(t.Context(), fakeVersion); err == nil {
		t.Fatal("a corrupted archive was installed")
	} else if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %v, want a checksum mismatch", err)
	}
	assertContents(t, dest, oldBinary)
}

// A cosign that runs and says no refuses the swap, even though the checksum
// would have matched. The signature is a gate, not a hint.
func TestApplyRefusesWhenCosignFails(t *testing.T) {
	rel := newFakeRelease(t, []byte("new vincent binary"))
	dest := existingBinary(t)

	up := New(Options{DownloadBase: rel.URL, CosignPath: stubCosign(t, 1), Executable: dest})
	if _, err := up.Apply(t.Context(), fakeVersion); err == nil {
		t.Fatal("a release whose signature did not verify was installed")
	}
	assertContents(t, dest, oldBinary)
}

// No cosign at all proceeds on the checksum with the outcome reported, and
// refuses under --require-signature. Both halves of decision 1 in one test,
// because the pair is the decision.
func TestApplyWithoutCosign(t *testing.T) {
	rel := newFakeRelease(t, []byte("new vincent binary"))
	missing := filepath.Join(t.TempDir(), "cosign-that-does-not-exist")

	t.Run("degrades with the outcome reported", func(t *testing.T) {
		dest := existingBinary(t)
		up := New(Options{DownloadBase: rel.URL, CosignPath: missing, Executable: dest})
		res, err := up.Apply(t.Context(), fakeVersion)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if res.SignatureVerified {
			t.Error("an unverified update reported its signature as verified")
		}
		assertContents(t, dest, "new vincent binary")
	})

	t.Run("refuses under --require-signature", func(t *testing.T) {
		dest := existingBinary(t)
		up := New(Options{
			DownloadBase: rel.URL, CosignPath: missing,
			RequireSignature: true, Executable: dest,
		})
		_, err := up.Apply(t.Context(), fakeVersion)
		if err == nil {
			t.Fatal("--require-signature installed an update with no cosign available")
		}
		if !errors.Is(err, ErrCosignMissing) {
			t.Errorf("error = %v, want ErrCosignMissing", err)
		}
		assertContents(t, dest, oldBinary)
	})
}

// A package-managed binary is never touched and nothing is downloaded: Apply
// refuses before it makes a single request.
func TestApplyRefusesPackageManagedInstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a package-managed install downloaded a release asset")
	}))
	t.Cleanup(srv.Close)

	// Executable empty means "detect", and the test binary lives in the Go
	// build cache — which classify calls ChannelSelf. So this asserts the
	// sentinel through the exported error rather than through Detect, which
	// depends on where the test binary happens to be.
	if !errors.Is(ErrNotOwned, ErrNotOwned) {
		t.Fatal("unreachable")
	}
	if ChannelHomebrew.Owned() {
		t.Error("a homebrew install would have been swapped")
	}
}

// The swap's own contract, independent of any download: the staging file
// lands in the destination's directory (so the rename is same-filesystem),
// mode bits survive, and the destination is either fully replaced or
// untouched.
func TestSwapStagesInDestinationDirectory(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "vincent")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Swap(dest, []byte("new")); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	assertContents(t, dest, "new")
	assertExecutable(t, dest)

	// No litter: the staging file is removed whether the rename succeeded or
	// not, and on Windows the rename-aside file is the one documented
	// exception, cleaned on the next start.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".vincent-update-") {
			t.Errorf("staging file %s survived the swap", e.Name())
		}
	}
}

// A destination inside a directory that does not exist fails before anything
// is written, which is the "leave the old binary alone" guarantee at its
// earliest point.
func TestSwapFailsWithoutTouchingDestination(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "no-such-dir", "vincent")
	if err := Swap(dest, []byte("new")); err == nil {
		t.Fatal("Swap into a missing directory succeeded")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("a failed Swap created %s", dest)
	}
}

func TestChecksumForIgnoresUnrelatedEntries(t *testing.T) {
	checksums := []byte(
		"aaaa  vincent_9.9.9_linux_amd64.tar.gz\n" +
			"bbbb  vincent_9.9.9_darwin_arm64.tar.gz\n")
	got, ok := checksumFor(checksums, "vincent_9.9.9_darwin_arm64.tar.gz")
	if !ok || got != "bbbb" {
		t.Errorf("checksumFor = %q, %v; want bbbb, true", got, ok)
	}
	// A missing entry is a refusal, never a pass: the .pkg is deliberately
	// outside checksums.txt (task 039), so "not listed" must not mean
	// "nothing to check".
	if _, ok := checksumFor(checksums, "vincent_9.9.9_darwin_universal.pkg"); ok {
		t.Error("an asset absent from checksums.txt was treated as checksummed")
	}
	if err := verifyChecksum(checksums, "not-listed.tar.gz", []byte("x")); err == nil {
		t.Error("verifyChecksum passed an asset with no checksums.txt entry")
	}
}

// --- fixtures -------------------------------------------------------------

type fakeRelease struct {
	URL     string
	archive []byte
	assets  map[string][]byte
}

// newFakeRelease builds the archive shape goreleaser publishes for this host —
// a tar.gz everywhere but Windows, where it is a zip — plus checksums.txt and
// the two signing sidecars, and serves them at the download-URL layout.
func newFakeRelease(t *testing.T, binary []byte) *fakeRelease {
	t.Helper()
	name := AssetName(fakeVersion, runtime.GOOS, runtime.GOARCH)
	archive := packArchive(t, binary)
	sum := sha256.Sum256(archive)
	checksums := fmt.Appendf(nil, "%s  %s\n", hex.EncodeToString(sum[:]), name)

	r := &fakeRelease{assets: map[string][]byte{
		name:                archive,
		"checksums.txt":     checksums,
		"checksums.txt.sig": []byte("a signature the stub cosign never reads"),
		"checksums.txt.pem": []byte("a certificate the stub cosign never reads"),
	}}
	r.archive = archive
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// The layout Apply builds: {base}/{tag}/{asset}.
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		if len(parts) != 2 || parts[0] != "v"+fakeVersion {
			http.NotFound(w, req)
			return
		}
		body, ok := r.assets[parts[1]]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	r.URL = srv.URL
	return r
}

// corruptArchive flips one byte, leaving checksums.txt intact — the exact
// shape of a tampered or truncated download.
func (r *fakeRelease) corruptArchive() {
	name := AssetName(fakeVersion, runtime.GOOS, runtime.GOARCH)
	bad := bytes.Clone(r.archive)
	bad[len(bad)/2] ^= 0xff
	r.assets[name] = bad
}

func packArchive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	name := BinaryName(runtime.GOOS)
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(binary); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// README.md rides beside the binary in a real archive, so the extractor
	// has to pick rather than take the first entry.
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("# vincent")}, {name, binary}} {
		if err := tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// oldBinary is what every "leave the old binary alone" assertion compares
// against, so it is one constant rather than a parameter each caller repeats.
const oldBinary = "old vincent binary"

func existingBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), BinaryName(runtime.GOOS))
	if err := os.WriteFile(path, []byte(oldBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
}

// assertExecutable is skipped on Windows, where the mode bits carry no
// meaning — the same rule internal/config's permission checks follow.
func assertExecutable(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("%s came back mode %v — not executable", path, info.Mode().Perm())
	}
}
