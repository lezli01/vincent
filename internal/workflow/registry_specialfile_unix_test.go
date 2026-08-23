//go:build unix

package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Issue #136: workflow discovery accepts every non-directory entry whose name
// ends in .yaml/.yml, so a symlink or a special file placed in a registered
// repo's .vincent/workflows is opened and read while the scope is catalogued —
// before any human picks a workflow. A directory scan that only asks
// DirEntry.IsDir cannot tell a regular file from a symlink or a FIFO (§5.2:
// the scopes are `*.yaml` files, and an unusable one is surfaced as a registry
// error "without breaking valid ones").

// TestLoadDirRejectsNonRegularFiles covers the two shapes that matter on unix:
// a symlink, whose target must not be read at all, and a FIFO, whose open
// blocks the loader forever.
func TestLoadDirRejectsNonRegularFiles(t *testing.T) {
	t.Run("symlink is not followed", func(t *testing.T) {
		reg, globalDir := newTestRegistry(t)
		writeWorkflow(t, globalDir, "good", manualWorkflow("good", "regular"))

		// A valid workflow that lives outside the scope entirely.
		outside := writeWorkflow(t, filepath.Join(t.TempDir(), "elsewhere"), "outside",
			manualWorkflow("sneaky", "target-content-marker"))
		link := filepath.Join(globalDir, "link.yaml")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unsupported here: %v", err)
		}

		reg.ReloadGlobal()

		for _, e := range reg.List(0) {
			if e.File == link && e.Valid() {
				t.Errorf("entry %q from %s is valid; a symlink must not be catalogued as a workflow", e.Name, link)
			}
			if strings.Contains(e.Source, "target-content-marker") {
				t.Errorf("entry %q captured the symlink target's content; the target must not be read", e.Name)
			}
		}
		if _, ok := reg.Lookup(0, "sneaky"); ok {
			t.Error(`Lookup("sneaky") resolved; the workflow behind the symlink must not be loaded`)
		}
		// The rejected sibling must not cost the valid file its entry.
		if e, ok := reg.Lookup(0, "good"); !ok || !e.Valid() {
			t.Errorf(`Lookup("good") = %+v, %v; the regular file must stay available`, e, ok)
		}
	})

	t.Run("fifo does not block cataloguing", func(t *testing.T) {
		reg, globalDir := newTestRegistry(t)
		writeWorkflow(t, globalDir, "good", manualWorkflow("good", "regular"))

		fifo := filepath.Join(globalDir, "hang.yaml")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Skipf("mkfifo unsupported here: %v", err)
		}

		// os.ReadFile on a FIFO with no writer blocks in open(2) forever, so
		// the reload has to be given a deadline rather than simply called.
		done := make(chan struct{})
		go func() {
			defer close(done)
			reg.ReloadGlobal()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			// The goroutine stays parked in open(2); the test process exits
			// regardless, and the FIFO goes with the temp dir.
			t.Fatalf("ReloadGlobal blocked on the FIFO %s; a scope with a special file must still catalogue", fifo)
		}

		if e, ok := reg.Lookup(0, "good"); !ok || !e.Valid() {
			t.Errorf(`Lookup("good") = %+v, %v; the regular file must stay available`, e, ok)
		}
		for _, e := range reg.List(0) {
			if e.File == fifo && e.Valid() {
				t.Errorf("entry %q from %s is valid; a FIFO must not be catalogued as a workflow", e.Name, fifo)
			}
		}
	})
}
