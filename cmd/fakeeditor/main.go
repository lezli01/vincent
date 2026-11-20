// Command fakeeditor is a stand-in for $EDITOR in tests (task 157): it is
// started the way the TUI starts a real editor — the editor's argv, then
// the file — and does one scripted thing to the file, so the composer's
// round trip can be proven against a real process on every platform.
//
// FAKEEDITOR_MODE picks the thing:
//
//	replace  write FAKEEDITOR_TEXT plus the newline an editor adds on save
//	keep     leave the file untouched
//	empty    truncate the file
//	fail     touch nothing and exit 3, as an editor that crashed would
//
// Arguments before the file are ignored, so a test can prove that a quoted
// $EDITOR with flags was split into the argv it meant. FAKEEDITOR_ARGS, when
// set, names a file the received argv is written to, one per line.
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "fakeeditor: no file to edit")
		os.Exit(2)
	}
	if log := os.Getenv("FAKEEDITOR_ARGS"); log != "" {
		//nolint:gosec // G703: a test double writes where its test told it to
		if err := os.WriteFile(log, []byte(strings.Join(os.Args[1:], "\n")+"\n"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "fakeeditor:", err)
			os.Exit(2)
		}
	}
	path := os.Args[len(os.Args)-1]
	var err error
	switch mode := os.Getenv("FAKEEDITOR_MODE"); mode {
	case "replace":
		err = os.WriteFile(path, []byte(os.Getenv("FAKEEDITOR_TEXT")+"\n"), 0o600) //nolint:gosec // G703: the file it was asked to edit
	case "keep", "":
	case "empty":
		err = os.WriteFile(path, nil, 0o600) //nolint:gosec // G703: the file it was asked to edit
	case "fail":
		os.Exit(3)
	default:
		err = fmt.Errorf("unknown FAKEEDITOR_MODE %q", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeeditor:", err)
		os.Exit(2)
	}
}
