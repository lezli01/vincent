package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// Run launches the TUI and blocks until the user quits (§12.1: bare
// `vincent`). The daemon keeps running after quit; if none is reachable at
// launch, the shell auto-starts one in the background.
func Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // stops the SSE stream goroutine with the program

	p := tea.NewProgram(newRoot(ctx, defaultConnector()), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}
	return nil
}
