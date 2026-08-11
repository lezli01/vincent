//go:build darwin

package service

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
)

// TestRenderPlist checks the launchd plist without a launchd to load it. The
// XML is parsed rather than only substring-matched: a malformed plist is
// rejected by launchd with a message that says nothing useful, so catching it
// here is worth the extra assertion.
func TestRenderPlist(t *testing.T) {
	plist := renderPlist(Options{
		Exe:  "/usr/local/bin/vincent",
		Dirs: config.Dirs{Config: "/Users/u/Library/Application Support/vincent", Data: "/Users/u/Library/vincent"},
		Path: "/opt/homebrew/bin:/Users/u/.local/bin:/usr/bin:/bin",
	})

	if err := xml.Unmarshal([]byte(plist), new(struct {
		XMLName xml.Name `xml:"plist"`
	})); err != nil {
		t.Fatalf("plist is not well-formed XML: %v\n%s", err, plist)
	}

	for _, want := range []string{
		"<string>" + LaunchdName + "</string>",
		"<string>/usr/local/bin/vincent</string>",
		"<string>daemon</string>",
		"VINCENT_CONFIG_DIR",
		"VINCENT_DATA_DIR",
		"<key>RunAtLoad</key><true/>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist is missing %q:\n%s", want, plist)
		}
	}

	// KeepAlive must be conditional on a clean exit. An unconditional
	// <true/> would relaunch a daemon that was deliberately stopped.
	if !strings.Contains(plist, "<key>SuccessfulExit</key><false/>") {
		t.Errorf("KeepAlive is not conditional on a clean exit:\n%s", plist)
	}

	// Without PATH, launchd's own minimal one applies and exec.LookPath finds
	// no agent CLI at all — the daemon runs, and every adapter reports missing
	// (T4.15).
	if !strings.Contains(plist, "<key>PATH</key><string>/opt/homebrew/bin:/Users/u/.local/bin:/usr/bin:/bin</string>") {
		t.Errorf("plist does not carry the install-time PATH:\n%s", plist)
	}
}

// TestRenderPlistEscapes: a PATH entry containing `&` or `<` is not exotic,
// and an unescaped one makes launchd reject the plist with a message that
// names neither the character nor the key.
func TestRenderPlistEscapes(t *testing.T) {
	plist := renderPlist(Options{
		Exe:  "/opt/a&b/vincent",
		Dirs: config.Dirs{Config: "/c", Data: "/d"},
		Path: "/usr/bin:/opt/r&d/bin",
	})

	if err := xml.Unmarshal([]byte(plist), new(struct {
		XMLName xml.Name `xml:"plist"`
	})); err != nil {
		t.Fatalf("plist is not well-formed XML: %v\n%s", err, plist)
	}
	if strings.Contains(plist, "r&d") {
		t.Errorf("ampersand was not escaped:\n%s", plist)
	}
	if !strings.Contains(plist, "/opt/r&amp;d/bin") {
		t.Errorf("escaped PATH is missing:\n%s", plist)
	}
}
