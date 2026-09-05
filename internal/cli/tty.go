package cli

import (
	"io"
	"os"

	"github.com/charmbracelet/x/term"
)

// isTTY reports whether a writer is attached to a terminal.
//
// It is the repository's only TTY test, and it exists for one caller: the
// in-progress indicator `vincent chat send` draws while it waits (task 089),
// which must not appear when stderr is a pipe or a file.
//
// The *os.File assertion is load-bearing on its own, before term.IsTerminal is
// consulted at all. Cobra's test writers are *bytes.Buffer, so every command
// test is non-TTY structurally rather than by mocking a predicate — which is
// what makes "redirected output is byte-identical" a property of the code
// rather than of the test setup.
//
// term is github.com/charmbracelet/x/term rather than golang.org/x/term: it is
// already in the module graph via bubbletea, so this adds no module and no new
// supply-chain surface, and it is the library the TUI already leans on for
// exactly this question. It owns the platform difference, so this file needs
// no _windows.go twin.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(f.Fd())
}
