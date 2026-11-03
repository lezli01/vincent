// Package jiratest builds and starts the cmd/fakejira test binary for Jira
// tests. It is imported only from _test files, and exists for the reason
// githubtest does: the stand-alone server must be exercised on Windows,
// macOS and Linux without a real Jira and without the network.
package jiratest

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/jira/fakejira"
	"github.com/lezli01/vincent/internal/testutil/wait"
)

// buildOnce compiles fakejira at most once per test process. The binary lands
// in a process-lifetime temp dir (the OS reaps it; per-test cleanup would
// break later tests in the same process).
var buildOnce = sync.OnceValues(build)

// BuildFakeJira returns the path to a compiled cmd/fakejira binary, failing
// (or skipping, when go itself is unavailable) the test otherwise.
func BuildFakeJira(t testing.TB) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	path, err := buildOnce()
	if err != nil {
		t.Fatalf("build fakejira: %v", err)
	}
	return path
}

func build() (string, error) {
	_, self, _, _ := runtime.Caller(0)
	// internal/jira/jiratest → repo root
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(self))))
	dir, err := os.MkdirTemp("", "vincent-fakejira-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "fakejira")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/fakejira")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", &buildError{output: string(b), err: err}
	}
	return out, nil
}

type buildError struct {
	output string
	err    error
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.output }

func (e *buildError) Unwrap() error { return e.err }

// Options are a fakejira server's files and credentials. Empty file fields
// get a file in t.TempDir(); empty credentials are the fake's defaults.
type Options struct {
	Corpus       string
	ScenarioFile string
	RequestsFile string
	User, Token  string
}

// Server is a running fakejira.
type Server struct {
	// URL is the site's origin, `http://127.0.0.1:PORT`.
	URL     string
	Options Options
	Cmd     *exec.Cmd
	// Stderr collects the server's standard error.
	Stderr *bytes.Buffer

	exited chan struct{}
	err    error
}

// StartFakeJira launches cmd/fakejira on 127.0.0.1:0, waits for its port
// file and registers a cleanup that stops it through the shutdown route,
// killing it only if it has not exited within wait.Timeout.
func StartFakeJira(t testing.TB, opts Options) *Server {
	t.Helper()
	bin := BuildFakeJira(t)
	dir := t.TempDir()
	if opts.Corpus == "" {
		opts.Corpus = filepath.Join(dir, "corpus.json")
	}
	if opts.ScenarioFile == "" {
		opts.ScenarioFile = filepath.Join(dir, "scenario")
	}
	if opts.RequestsFile == "" {
		opts.RequestsFile = filepath.Join(dir, "requests.log")
	}
	if opts.User == "" && opts.Token == "" {
		opts.User, opts.Token = fakejira.DefaultUser, fakejira.DefaultToken
	}
	portFile := filepath.Join(dir, "port")
	cmd := exec.Command(bin, "serve",
		"--corpus", opts.Corpus, "--scenario-file", opts.ScenarioFile,
		"--requests-file", opts.RequestsFile, "--port-file", portFile,
		"--addr", "127.0.0.1:0", "--user", opts.User, "--token", opts.Token)
	srv := &Server{Options: opts, Cmd: cmd, Stderr: &bytes.Buffer{}, exited: make(chan struct{})}
	cmd.Stderr = srv.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fakejira: %v", err)
	}
	go func() {
		srv.err = cmd.Wait()
		close(srv.exited)
	}()
	t.Cleanup(func() {
		if srv.Exited() {
			return
		}
		srv.Shutdown()
		if !srv.WaitExit(wait.Timeout(10 * time.Second)) {
			_ = cmd.Process.Kill()
			<-srv.exited
		}
	})
	var port int
	wait.Until(t, "fakejira writes its port file", func() bool {
		if srv.Exited() {
			return true
		}
		raw, err := os.ReadFile(portFile)
		if err != nil {
			return false
		}
		port, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil
	})
	if srv.Exited() {
		t.Fatalf("fakejira exited before serving: %v\n%s", srv.err, srv.Stderr)
	}
	srv.URL = "http://127.0.0.1:" + strconv.Itoa(port)
	return srv
}

// shutdownTimeout bounds the shutdown request. A wedged server that takes
// the connection and never answers must not hold the cleanup: past this the
// request fails and the cleanup falls through to killing the process (review
// F12). A var so the test can shorten it.
var shutdownTimeout = wait.Timeout(5 * time.Second)

// Shutdown asks the server to stop through POST /_fake/shutdown, reporting
// whether the route accepted. It gives up after shutdownTimeout.
func (s *Server) Shutdown() bool {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+fakejira.ShutdownPath, http.NoBody)
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusAccepted
}

// Exited reports whether the process has exited.
func (s *Server) Exited() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}

// WaitExit waits up to d for the process to exit.
func (s *Server) WaitExit(d time.Duration) bool {
	select {
	case <-s.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// ExitErr is the process's exit error once it has exited; nil is exit 0.
func (s *Server) ExitErr() error {
	<-s.exited
	return s.err
}
