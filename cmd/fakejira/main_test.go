package main

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/jira/fakejira"
	"github.com/lezli01/vincent/internal/jira/jiratest"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

func get(t *testing.T, srv *jiratest.Server, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(srv.Options.User, srv.Options.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestServeAndShutdownRoute is the cross-platform contract: start on :0,
// read the port file, answer an authenticated request, log it, and exit 0
// through POST /_fake/shutdown.
func TestServeAndShutdownRoute(t *testing.T) {
	srv := jiratest.StartFakeJira(t, jiratest.Options{User: "ada@example.com", Token: "s3cret"})
	if got := get(t, srv, "/rest/api/3/myself"); got != http.StatusOK {
		t.Fatalf("myself: %d", got)
	}
	if n, err := fakejira.CountRequests(srv.Options.RequestsFile, "GET", "rest/api/3/myself", 200); err != nil || n != 1 {
		t.Fatalf("request log counted %d (%v)", n, err)
	}
	if !srv.Shutdown() {
		t.Fatal("shutdown route refused")
	}
	if !srv.WaitExit(wait.Timeout(10 * time.Second)) {
		t.Fatal("fakejira did not exit after the shutdown route")
	}
	if err := srv.ExitErr(); err != nil {
		t.Fatalf("exit: %v\n%s", err, srv.Stderr)
	}
}

func TestEnvCredentials(t *testing.T) {
	t.Setenv("FAKEJIRA_USER", "env@example.com")
	t.Setenv("FAKEJIRA_API_TOKEN", "env-token")
	if !strings.Contains(usageFlags(t), "env@example.com") {
		t.Fatal("FAKEJIRA_USER is not the --user default")
	}
}

// usageFlags renders the serve flag defaults by asking for help.
func usageFlags(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	if code := run([]string{"serve", "-h"}, &b); code != 2 {
		t.Fatalf("serve -h exited %d", code)
	}
	return b.String()
}

func TestRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.0.2.1:0", "example.com:0"} {
		var b strings.Builder
		if code := run([]string{"serve", "--addr", addr}, &b); code != 1 || !strings.Contains(b.String(), "non-loopback") {
			t.Errorf("%s: exit %d: %s", addr, code, b.String())
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		if !loopback(addr) {
			t.Errorf("%s refused", addr)
		}
	}
	if code := run(nil, io.Discard); code != 2 {
		t.Errorf("no subcommand exited %d", code)
	}
}

// TestMutationSubcommands: seed writes the built-in corpus, and move, delete
// and forbid change it through the Store's helpers — the corpus a serving
// fakejira then answers from.
func TestMutationSubcommands(t *testing.T) {
	corpus := filepath.Join(t.TempDir(), "corpus.json")
	mut := func(args ...string) (int, string) {
		var b strings.Builder
		code := run(append([]string{args[0], "--corpus", corpus}, args[1:]...), &b)
		return code, b.String()
	}
	if code, out := mut("seed"); code != 0 {
		t.Fatalf("seed exited %d: %s", code, out)
	}
	if code, out := mut("move", "VIN-1", "OPS", "OPS-7"); code != 0 {
		t.Fatalf("move exited %d: %s", code, out)
	}
	if code, out := mut("delete", "VIN-2"); code != 0 {
		t.Fatalf("delete exited %d: %s", code, out)
	}
	if code, out := mut("forbid", "VIN-3"); code != 0 {
		t.Fatalf("forbid exited %d: %s", code, out)
	}
	// seed over an existing file keeps what is there.
	if code, out := mut("seed"); code != 0 {
		t.Fatalf("second seed exited %d: %s", code, out)
	}
	c, err := (&fakejira.Store{Path: corpus}).Load()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]fakejira.Issue{}
	for _, is := range c.Issues {
		got[is.ID] = is
	}
	if is := got["10001"]; is.Key != "OPS-7" || is.Project != "OPS" || len(is.PreviousKeys) != 1 || is.PreviousKeys[0] != "VIN-1" {
		t.Errorf("moved VIN-1 = %+v", is)
	}
	if is := got["10002"]; is.Fake == nil || !is.Fake.Deleted {
		t.Errorf("deleted VIN-2 = %+v", is)
	}
	if is := got["10003"]; is.Fake == nil || !is.Fake.Forbidden {
		t.Errorf("forbidden VIN-3 = %+v", is)
	}

	if code, _ := mut("delete", "VIN-99"); code != 1 {
		t.Errorf("deleting an unknown issue exited %d, want 1", code)
	}
	if code, _ := mut("move", "OPS-7", "NOPE", "NOPE-1"); code != 1 {
		t.Errorf("moving into an unknown project exited %d, want 1", code)
	}
	if code, _ := mut("move", "OPS-7"); code != 2 {
		t.Errorf("move with one argument exited %d, want 2", code)
	}
	if code := run([]string{"delete", "VIN-1"}, io.Discard); code != 2 {
		t.Errorf("delete without --corpus exited %d, want 2", code)
	}
	if code := run([]string{"bogus"}, io.Discard); code != 2 {
		t.Errorf("an unknown subcommand exited %d, want 2", code)
	}
}
