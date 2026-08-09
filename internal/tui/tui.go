package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/config"
)

// Run launches the TUI and blocks until the user quits (§12.1: bare
// `vincent`). The daemon keeps running after quit; if none is reachable at
// launch, the shell auto-starts one in the background.
func Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // stops the SSE stream goroutine with the program

	// Resolved once, here, rather than inside the connect flow: the §16
	// first-run notice renders before the daemon is reached and has to name
	// the same directory the connector will use. A failure is not fatal yet
	// — it surfaces as the connect error, and an unresolved dir means the
	// notice is shown.
	dirs, dirsErr := config.ResolveDirs()
	dataDir := dirs.Data
	if dirsErr != nil {
		dataDir = ""
	}
	cn := defaultConnector()
	cn.resolveDataDir = func() (string, error) {
		if dirsErr != nil {
			return "", fmt.Errorf("resolve data dir: %w", dirsErr)
		}
		return dataDir, nil
	}

	final, err := tea.NewProgram(newRoot(ctx, cn, dataDir), tea.WithContext(ctx)).Run()
	if err != nil {
		return fmt.Errorf("run tui: %w", err)
	}
	// Printed after the alt screen has torn down, which is what leaves the
	// line in the scrollback — the point of reminding on the way out.
	if m, ok := final.(*root); ok {
		if line, ok := m.quitReminder(); ok {
			fmt.Println(line)
		}
	}
	return nil
}
