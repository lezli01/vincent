//go:build darwin || windows

package service

import (
	"encoding/xml"
	"strings"
)

// xmlEscape escapes a value substituted into the launchd plist or the Windows
// task definition. Both are XML the OS parses before it tells you anything
// useful: launchd rejects a malformed plist with a message naming neither the
// character nor the key, and Task Scheduler answers "the task XML contains a
// value which is incorrectly formatted or out of range" for the whole file.
// A directory containing `&` is unusual; a PATH or an argument list containing
// one is not.
func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		// strings.Builder never fails to write.
		return s
	}
	return b.String()
}
