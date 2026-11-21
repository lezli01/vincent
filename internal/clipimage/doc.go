// Package clipimage reads an image off the system clipboard, and recognizes a
// pasted path that names one (task 170).
//
// It runs in the client, never the daemon, for the reason internal/nerdfont
// does: the clipboard belongs to the machine with the keyboard, and the
// daemon may be anywhere a localhost API reaches.
//
// Nothing here links a clipboard library. Each platform already ships, or
// commonly has, a program that can hand the clipboard's image to a pipe —
// osascript on macOS, wl-paste or xclip on Linux, PowerShell on Windows — and
// Read runs that program, bounded by the caller's context and by MaxBytes.
// The price is that a machine without the helper has no image paste, and that
// is reported exactly as an empty clipboard is: ErrNoImage. The caller's
// fallback is the ordinary text paste, which needs no explanation, so a
// missing helper is not worth an error the person has to read.
//
// It is a leaf: it imports the standard library only.
package clipimage
