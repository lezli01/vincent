package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/github/githubtest"
)

// run invokes the built fakegh with env on top of the test's, and reports
// stdout, stderr and the exit code.
func run(t *testing.T, gh string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(gh, args...)
	cmd.Env = append(os.Environ(), env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatalf("run %v: %v", args, err)
	}
	return out.String(), errOut.String(), code
}

// header reads one header out of `gh api -i` output.
func header(out, name string) string {
	for _, line := range strings.Split(out, "\r\n") {
		if k, v, ok := strings.Cut(line, ": "); ok && k == name {
			return v
		}
	}
	return ""
}

// A conditional hit answers gh 2.100.0's observed 304 to the byte: the
// status line, the etag and an unchanged rate limit, a blank line and no
// body on stdout; `gh: HTTP 304` on stderr; exit 1.
func TestAPINotModifiedShape(t *testing.T) {
	gh := githubtest.BuildFakeGH(t)
	env := []string{
		"FAKEGH_ISSUES_FILE=" + filepath.Join(t.TempDir(), "issues.json"),
		"FAKEGH_SCENARIO=", "FAKEGH_SCENARIO_FILE=", "FAKEGH_REPO=",
	}
	const endpoint = "repos/octo/repo/issues?state=all&per_page=1&sort=updated&direction=desc"

	first, _, code := run(t, gh, env, "api", "-i", endpoint)
	if code != 0 || !strings.HasPrefix(first, "HTTP/2.0 200 OK\n") {
		t.Fatalf("first call: exit %d\n%s", code, first)
	}
	etag := header(first, "Etag")
	if etag == "" {
		t.Fatalf("no Etag in\n%s", first)
	}

	stdout, stderr, code := run(t, gh, env, "api", "-i", "-H", "If-None-Match: "+etag, endpoint)
	want := "HTTP/2.0 304 Not Modified\n" +
		"Etag: " + etag + "\r\n" +
		"X-Ratelimit-Limit: " + header(first, "X-Ratelimit-Limit") + "\r\n" +
		"X-Ratelimit-Remaining: " + header(first, "X-Ratelimit-Remaining") + "\r\n" +
		"X-Ratelimit-Reset: " + header(stdout, "X-Ratelimit-Reset") + "\r\n" +
		"X-Ratelimit-Resource: core\r\n" +
		"X-Ratelimit-Used: " + header(first, "X-Ratelimit-Used") + "\r\n" +
		"\r\n"
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
	if stderr != "gh: HTTP 304\n" {
		t.Errorf("stderr = %q", stderr)
	}
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

// A porcelain close writes the corpus `gh api` reads, and the scenario file
// flips the next invocation to unreachable.
func TestIssueCloseAndScenarioFile(t *testing.T) {
	gh := githubtest.BuildFakeGH(t)
	dir := t.TempDir()
	scenario := filepath.Join(dir, "scenario")
	env := []string{
		"FAKEGH_ISSUES_FILE=" + filepath.Join(dir, "issues.json"),
		"FAKEGH_SCENARIO=", "FAKEGH_SCENARIO_FILE=" + scenario, "FAKEGH_REPO=",
	}

	_, stderr, code := run(t, gh, env, "issue", "close", "41", "-R", "octo/repo", "--reason", "not planned")
	if code != 0 || stderr != "✓ Closed issue octo/repo#41 (Board header truncates on narrow terminals)\n" {
		t.Fatalf("close: exit %d %q", code, stderr)
	}
	body, _, code := run(t, gh, env, "api", "repos/octo/repo/issues/41")
	if code != 0 || !strings.Contains(body, `"state":"closed"`) || !strings.Contains(body, `"state_reason":"not_planned"`) {
		t.Errorf("api after close: exit %d %s", code, body)
	}
	list, _, _ := run(t, gh, env, "issue", "list", "--repo", "octo/repo", "--state", "open", "--json", "number")
	if strings.Contains(list, `"number":41`) {
		t.Errorf("closed issue still listed open: %s", list)
	}

	if err := os.WriteFile(scenario, []byte("unreachable"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = run(t, gh, env, "api", "repos/octo/repo/issues")
	if code != 1 || !strings.HasPrefix(stderr, "error connecting to api.github.com\n") {
		t.Errorf("unreachable: exit %d %q", code, stderr)
	}
	if err := os.WriteFile(scenario, []byte("rate-limited"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := run(t, gh, env, "api", "-i", "repos/octo/repo/issues")
	if code != 1 || stderr != "gh: API rate limit exceeded for user ID 1. (HTTP 403)\n" ||
		header(stdout, "X-Ratelimit-Remaining") != "0" || header(stdout, "Retry-After") == "" {
		t.Errorf("rate-limited: exit %d %q\n%s", code, stderr, stdout)
	}
}
