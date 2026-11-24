package tui

import (
	"strings"
	"testing"
)

// #1080: the palette reopens the full-auto notice, and esc closes it again
// without touching what was recorded.
func TestPaletteReopensTheNotice(t *testing.T) {
	m := connectedRoot(t)
	m.openPalette()
	var row *paletteEntry
	m.palette.input.SetValue("full-auto notice")
	for _, e := range m.palette.matches() {
		if strings.HasPrefix(e.label, "show the full-auto notice again") {
			row = &e
		}
	}
	if row == nil {
		t.Fatal("the palette has no row reopening the notice")
	}
	m.palette = nil
	m.Update(row.action())
	if !m.notice.active || !strings.Contains(m.notice.render(), "reopened") {
		t.Fatal("the row did not reopen the notice")
	}
	m.updateKey(keyPress("esc"))
	if m.notice.active {
		t.Fatal("esc did not close a reopened notice")
	}
}

// The first notice still takes enter alone: esc is what people press to
// make a box go away without reading it.
func TestFirstNoticeIgnoresEsc(t *testing.T) {
	m := connectedRoot(t)
	m.notice.active = true
	m.updateKey(keyPress("esc"))
	if !m.notice.active {
		t.Fatal("esc dismissed the first-run notice")
	}
}
