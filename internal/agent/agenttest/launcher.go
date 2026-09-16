package agenttest

import (
	"bytes"
	"io"
	"sync"

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

	mu       sync.Mutex
	launches []*Launch
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
