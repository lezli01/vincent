package release

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveMaster answers GitHub's release-by-tag route for MasterTag with body,
// and 404s every other path, so a client that asked for /releases/latest
// or another tag fails the test.
func serveMaster(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+Repo+"/releases/tags/"+MasterTag {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// masterBody is the shape GitHub serves for a release by tag, trimmed to
// what Master reads plus the sidecars a real one carries. It is written by
// hand: the first master-latest release is published by the workflow this
// task adds.
func masterBody(archives ...string) string {
	assets := []string{`{"name":"checksums.txt"}`, `{"name":"checksums.txt.sig"}`, `{"name":"checksums.txt.pem"}`}
	for _, a := range archives {
		assets = append(assets, `{"name":"`+a+`"}`)
	}
	return `{"tag_name":"master-latest","prerelease":true,"draft":false,
		"html_url":"https://github.com/lezli01/vincent/releases/tag/master-latest",
		"published_at":"2026-11-17T10:00:00Z","assets":[` + strings.Join(assets, ",") + `]}`
}

// Master reads the build's version off its archive names (task 147).
func TestMasterReadsTheBuildVersion(t *testing.T) {
	srv := serveMaster(t, http.StatusOK, masterBody(
		"vincent_0.21.1-master.5.gabc1234_darwin_arm64.tar.gz",
		"vincent_0.21.1-master.5.gabc1234_windows_amd64.zip",
	))
	rel, err := New(Options{BaseURL: srv.URL}).Master(t.Context())
	if err != nil {
		t.Fatalf("Master: %v", err)
	}
	if rel.Version != "v0.21.1-master.5.gabc1234" || rel.PublishedAt.IsZero() ||
		rel.URL != "https://github.com/lezli01/vincent/releases/tag/master-latest" {
		t.Errorf("Master = %+v", rel)
	}
	if !IsMasterBuild(rel.Version) || IsMasterBuild("v0.21.0") || IsMasterBuild("v0.21.1-rc.1") {
		t.Error("IsMasterBuild misreads a version")
	}
	// A master build is never what the stable check offers.
	if IsNewer("v0.21.0", rel.Version) {
		t.Error("IsNewer offered a master build on the release track")
	}
	// The release it precedes is newer than it.
	if !IsNewer(rel.Version, "v0.21.1") || IsNewer(rel.Version, "v0.21.0") {
		t.Error("a master build does not sort between the release before it and the one after")
	}
}

func TestMasterRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"no master build yet", `{"message":"Not Found"}`, "404", http.StatusNotFound},
		{"no archives yet", masterBody(), "no archives", http.StatusOK},
		{"a release version", masterBody("vincent_0.21.0_linux_amd64.tar.gz"), "master version", http.StatusOK},
		{"another tag", strings.Replace(masterBody("vincent_0.21.1-master.1.gaaaaaaa_linux_amd64.tar.gz"),
			`"tag_name":"master-latest"`, `"tag_name":"v0.21.0"`, 1), "not published under", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveMaster(t, tc.status, tc.body)
			_, err := New(Options{BaseURL: srv.URL}).Master(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Master error = %v, want one naming %q", err, tc.want)
			}
		})
	}
}
