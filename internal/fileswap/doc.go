// Package fileswap completes an atomic file write: it puts a finished
// temporary file in place of its target with a given mode, chmod and rename
// as one step.
//
// On unix that is one attempt. On Windows a rename cannot replace a file
// another handle has open without FILE_SHARE_DELETE, which Go's os.Open does
// not pass, and the daemon opens its own workflow, config and trigger files
// that way on every reload — so Replace waits out a contended handle for a
// bounded window rather than failing the write (spec §12.3: the write is
// atomic; issue #969).
//
// It is a leaf: it imports the standard library and golang.org/x/sys/windows
// only, so internal/config — itself a leaf — can use it beside
// internal/workflow.
package fileswap
