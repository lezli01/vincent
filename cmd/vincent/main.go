// Command vincent is the entry point for the vincent local AI workload
// orchestrator: one binary serving the TUI, the daemon, and thin API clients
// (spec §12.1).
package main

import (
	"os"

	// The embedded IANA time zone database, ~450 KB in the binary, for one
	// reason: a `type: schedule` trigger's `timezone:` (task 121). time.Local
	// comes from the Windows registry, so the *default* zone works there, but
	// time.LoadLocation reads no tz database on Windows — without this a
	// named zone such as Europe/Budapest would load on Linux and macOS and
	// refuse on Windows, which cross-platform being a hard requirement
	// (CLAUDE.md) does not allow. Imported here rather than in
	// internal/trigger so a test binary linking that package cannot pass on
	// a database the shipped binary does not have.
	_ "time/tzdata"

	"github.com/lezli01/vincent/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
