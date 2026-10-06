//go:build linux

package procx

import "fmt"

// zombie reports whether pid has terminated but not yet been reaped: state
// Z in /proc/<pid>/stat, or X, the dead state a process passes through on
// its way out of the table. Returns ErrProcessGone when no such process
// exists.
func zombie(pid int) (bool, error) {
	fields, err := statFields(pid)
	if err != nil {
		return false, err
	}
	if len(fields) == 0 {
		return false, fmt.Errorf("process %d stat: too few fields", pid)
	}
	switch fields[0] { // field 3, the state
	case "Z", "X", "x":
		return true, nil
	}
	return false, nil
}
