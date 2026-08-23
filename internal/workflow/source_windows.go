package workflow

import "os"

// sourceOpenFlags opens a workflow file for reading. Windows has no
// O_NOFOLLOW and no filesystem FIFOs; a reparse point or junction named
// *.yaml is caught by the directory scan's type check, which Go fills in from
// the reparse-point attributes, and by the Stat on the opened handle
// (§5.2, issue #136).
const sourceOpenFlags = os.O_RDONLY
