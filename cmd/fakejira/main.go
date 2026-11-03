// Command fakejira is a stand-in Jira Cloud site for tests and the Jira gate
// (#914): it serves internal/jira/fakejira's Store — the same handler a test
// mounts behind httptest — on a real loopback listener. It is test
// infrastructure; nothing in vincent invokes it.
//
//	fakejira serve --corpus F --scenario-file S --requests-file R --port-file P \
//	    [--addr 127.0.0.1:0] [--user U --token T]
//
// --user and --token fall back to FAKEJIRA_USER and FAKEJIRA_API_TOKEN, and
// then to fakejira.DefaultUser and DefaultToken. A non-loopback --addr is
// refused: the fake is never reachable from another machine.
//
// The port file: fakejira binds first and only then writes the port it got
// to --port-file, through a temp file and a rename, so a reader polling for
// the file never sees half a number. A caller passing `:0` reads the port
// from there.
//
// Stopping it: `POST /_fake/shutdown` (loopback only, no Jira auth) closes the
// listener, lets the in-flight request finish and exits 0 on every OS. On
// POSIX, SIGTERM and os.Interrupt do the same. Windows has no graceful signal
// for a console-less child (internal/procx/procx_windows.go records why), so
// there the route is the contract, not CTRL_BREAK: a gate's cleanup must curl
// the route and wait for the pid to exit before it removes the directory the
// binary lives in, which is also what avoids Windows' "exe busy" race on
// cleanup. The request log is appended and closed per request, so there is
// nothing left to flush at exit.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/lezli01/vincent/internal/jira/fakejira"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "serve" {
		_, _ = fmt.Fprintln(stderr, "usage: fakejira serve --corpus F --scenario-file S --requests-file R --port-file P [--addr 127.0.0.1:0] [--user U --token T]")
		return 2
	}
	fl := flag.NewFlagSet("fakejira serve", flag.ContinueOnError)
	fl.SetOutput(stderr)
	corpus := fl.String("corpus", "", "corpus JSON file (seeded from the built-in corpus when missing)")
	scenarioFile := fl.String("scenario-file", "", "file holding the scenario, re-read on every request")
	requests := fl.String("requests-file", "", "request log, one `METHOD path status` line per request")
	portFile := fl.String("port-file", "", "file the bound port is written to")
	addr := fl.String("addr", "127.0.0.1:0", "loopback address to listen on")
	user := fl.String("user", os.Getenv("FAKEJIRA_USER"), "accepted Basic auth user")
	token := fl.String("token", os.Getenv("FAKEJIRA_API_TOKEN"), "accepted Basic auth API token")
	if err := fl.Parse(args[1:]); err != nil {
		return 2
	}
	if err := serve(*addr, *portFile, &fakejira.Store{
		Path: *corpus, ScenarioFile: *scenarioFile, RequestLog: *requests, User: *user, Token: *token,
	}); err != nil {
		_, _ = fmt.Fprintln(stderr, "fakejira:", err)
		return 1
	}
	return 0
}

// loopback reports whether addr's host is a loopback address or localhost.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func serve(addr, portFile string, store *fakejira.Store) error {
	if !loopback(addr) {
		return fmt.Errorf("refusing non-loopback address %q", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if portFile != "" {
		port := ln.Addr().(*net.TCPAddr).Port
		if err := fakejira.WriteFileAtomic(portFile, []byte(strconv.Itoa(port)+"\n")); err != nil {
			_ = ln.Close()
			return err
		}
	}
	stop := make(chan struct{})
	var once sync.Once
	store.OnShutdown = func() { once.Do(func() { close(stop) }) }
	srv := &http.Server{Handler: store, ReadHeaderTimeout: 10 * time.Second}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-stop:
	case <-ctx.Done():
	}
	shutdownCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
