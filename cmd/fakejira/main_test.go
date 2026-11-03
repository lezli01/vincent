package main

import (
	"io"
	"net/http"
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
