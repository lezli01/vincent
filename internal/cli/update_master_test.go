package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/release"
	"github.com/lezli01/vincent/internal/selfupdate"
)

// `vincent update --master` (task 147): the newest master build, from the
// rolling master-latest prerelease, through the release's own chain.

const masterTestVersion = "0.21.1-master.5.gabc1234"

// masterFeed serves the release-by-tag answer for master-latest naming
// this host's archive at version.
func masterFeed(t *testing.T, version string) *httptest.Server {
	t.Helper()
	asset := selfupdate.AssetName(version, runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+release.Repo+"/releases/tags/"+release.MasterTag {
			t.Errorf("--master asked the feed for %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"tag_name":%q,"prerelease":true,"published_at":"2026-11-17T10:00:00Z",
			"html_url":"https://github.com/lezli01/vincent/releases/tag/master-latest",
			"assets":[{"name":"checksums.txt"},{"name":%q}]}`, release.MasterTag, asset)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// masterDownloads serves the build's archive and checksums.txt at
// {base}/master-latest/{asset}, with no signature sidecars: the update then
// degrades to the checksum alone, as it does with no cosign installed,
// without reaching for one.
func masterDownloads(t *testing.T, version string, binary []byte) *httptest.Server {
	t.Helper()
	asset := selfupdate.AssetName(version, runtime.GOOS, runtime.GOARCH)
	archive := packTestArchive(t, binary)
	sum := sha256.Sum256(archive)
	files := map[string][]byte{
		asset:           archive,
		"checksums.txt": fmt.Appendf(nil, "%s  %s\n", hex.EncodeToString(sum[:]), asset),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tag, name, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		body, ok := files[name]
		if tag != release.MasterTag || !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func packTestArchive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	name := selfupdate.BinaryName(runtime.GOOS)
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, err := zw.Create(name)
		if err == nil {
			_, err = w.Write(binary)
		}
		if err == nil {
			err = zw.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// --master installs the master build over the binary, from its own tag.
func TestUpdateMasterSwapsTheMasterBuild(t *testing.T) {
	feed := masterFeed(t, masterTestVersion)
	downloads := masterDownloads(t, masterTestVersion, []byte("master build"))
	exe := filepath.Join(t.TempDir(), selfupdate.BinaryName(runtime.GOOS))
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, out := updateTestCmd(t)
	err := runUpdate(cmd, updateOpts{master: true, baseURL: feed.URL, downloadBase: downloads.URL, executable: exe})
	if err != nil {
		t.Fatalf("runUpdate --master: %v (%s)", err, out.String())
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "master build" {
		t.Fatalf("binary = %q, want the master build", got)
	}
	for _, want := range []string{"to vincent v" + masterTestVersion, "This is a master build", "NOT verified"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// --check --master reports the master build, and exits 2 for any build that
// is not it — a test binary is `dev`, which a plain update never calls
// behind but --master, the explicit request, does.
func TestUpdateMasterCheck(t *testing.T) {
	feed := masterFeed(t, masterTestVersion)
	cmd, out := updateTestCmd(t)
	cmd.SetArgs(nil)
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	err := runUpdate(cmd, updateOpts{master: true, checkOnly: true, baseURL: feed.URL})
	if got := exitCodeOf(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (%s)", got, out.String())
	}
	var body map[string]any
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("--json: %v (%s)", err, out.String())
	}
	if body["latest_version"] != "v"+masterTestVersion || body["track"] != "master" || body["update_available"] != true {
		t.Errorf("--check --master --json = %v", body)
	}
}

// A dry run downloads nothing.
func TestUpdateMasterDryRun(t *testing.T) {
	feed := masterFeed(t, masterTestVersion)
	downloads := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("--dry-run downloaded a master build asset")
	}))
	t.Cleanup(downloads.Close)
	cmd, out := updateTestCmd(t)
	err := runUpdate(cmd, updateOpts{
		master: true, dryRun: true, baseURL: feed.URL, downloadBase: downloads.URL,
		executable: filepath.Join(t.TempDir(), "vincent"),
	})
	if err != nil || !strings.Contains(out.String(), "Would download vincent v"+masterTestVersion) {
		t.Errorf("dry run = %v: %s", err, out.String())
	}
}

// A package manager carries releases only: --master on its install prints
// no upgrade command, which would install a release, and touches nothing.
func TestUpdateMasterOnPackageManagedInstall(t *testing.T) {
	cmd, out := updateTestCmd(t)
	rel := release.Release{Version: "v" + masterTestVersion, URL: "https://github.com/lezli01/vincent/releases/tag/master-latest"}
	err := updateNotOwned(cmd, out, "v0.21.0", rel, selfupdate.ChannelHomebrew, trackMaster)
	if got := exitCodeOf(err); got != 2 {
		t.Errorf("exit code = %d, want 2", got)
	}
	if strings.Contains(out.String(), selfupdate.ChannelHomebrew.UpgradeCommand()) ||
		!strings.Contains(out.String(), "carries releases only") {
		t.Errorf("output = %q", out.String())
	}
}
