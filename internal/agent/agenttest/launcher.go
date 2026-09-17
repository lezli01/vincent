package agenttest

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// RecordingLauncher is a host launcher that remembers what it was handed
// (task 062.1). An adapter test runs a real fakeagent through it and then
// asserts on each Command: that the seam is handed exactly what the adapter
// used to spawn is a claim about the Command, and the process still has to run
// for the adapter's Start to be the real one.
type RecordingLauncher struct {
	// OnLaunch, when set, runs before the host launch with the Command about
	// to start — the moment a test checks what must already be on disk.
	OnLaunch func(agent.Command)
	// OnResolve and OnProbe, when set, answer the launcher's pre-start hooks
	// in place of the host (task 062.2): a test that stands in for an image
	// says what its PATH holds and what its CLI reports, while Launch still
	// runs the real child on the host.
	OnResolve func(adapter, configured, binary string) (string, error)
	OnProbe   func(path string, args ...string) (stdout, stderr []byte, err error)

	mu       sync.Mutex
	launches []*Launch
	probes   [][]string
}

// Launch is one Command a RecordingLauncher started, and every byte that
// reached the child's stdin through it.
type Launch struct {
	Command agent.Command

	mu    sync.Mutex
	stdin bytes.Buffer
}

// Stdin returns what the child was given on stdin so far: the whole reader for
// a plain Command, everything written to the retained pipe for a StdinPipe
// one.
func (l *Launch) Stdin() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.stdin.Bytes())
}

func (l *Launch) record(p []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stdin.Write(p)
}

// Launch implements agent.Launcher.
func (r *RecordingLauncher) Launch(cmd agent.Command) (agent.Process, error) {
	rec := &Launch{Command: cmd}
	if cmd.Stdin != nil && !cmd.StdinPipe {
		// Read up front and hand the child a copy: the recorded bytes are
		// then exactly the child's, with no tee racing the process.
		b, err := io.ReadAll(cmd.Stdin)
		if err != nil {
			return nil, err
		}
		rec.record(b)
		cmd.Stdin = bytes.NewReader(b)
	}
	r.mu.Lock()
	r.launches = append(r.launches, rec)
	r.mu.Unlock()
	if r.OnLaunch != nil {
		r.OnLaunch(cmd)
	}
	p, err := agent.HostLauncher{}.Launch(cmd)
	if err != nil || !cmd.StdinPipe {
		return p, err
	}
	return recordedStdin{Process: p, rec: rec}, nil
}

// Resolve implements agent.Launcher: OnResolve when set, the host otherwise.
func (r *RecordingLauncher) Resolve(adapter, configured, binary string) (string, error) {
	if r.OnResolve != nil {
		return r.OnResolve(adapter, configured, binary)
	}
	return agent.HostLauncher{}.Resolve(adapter, configured, binary)
}

// Probe implements agent.Launcher: OnProbe when set, the host otherwise.
// Every probe is recorded, path first.
func (r *RecordingLauncher) Probe(
	ctx context.Context, timeout time.Duration, path string, args ...string,
) (stdout, stderr []byte, err error) {
	r.mu.Lock()
	r.probes = append(r.probes, append([]string{path}, args...))
	r.mu.Unlock()
	if r.OnProbe != nil {
		return r.OnProbe(path, args...)
	}
	return agent.HostLauncher{}.Probe(ctx, timeout, path, args...)
}

// Probes returns every probe's argv so far, in order.
func (r *RecordingLauncher) Probes() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.probes...)
}

// Launches returns every launch so far, in order.
func (r *RecordingLauncher) Launches() []*Launch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Launch(nil), r.launches...)
}

// recordedStdin tees a retained stdin pipe into its Launch.
type recordedStdin struct {
	agent.Process
	rec *Launch
}

func (p recordedStdin) Stdin() io.WriteCloser {
	return stdinTee{WriteCloser: p.Process.Stdin(), rec: p.rec}
}

type stdinTee struct {
	io.WriteCloser
	rec *Launch
}

func (t stdinTee) Write(b []byte) (int, error) {
	n, err := t.WriteCloser.Write(b)
	t.rec.record(b[:n])
	return n, err
}
