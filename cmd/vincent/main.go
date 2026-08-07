// Command vincent is the entry point for the vincent local AI workload
// orchestrator: one binary serving the TUI, the daemon, and thin API clients
// (spec §12.1).
package main

import (
	"os"

	"github.com/lezli01/vincent/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
