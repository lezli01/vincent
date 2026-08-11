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
}
