package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"

	"charm.land/lipgloss/v2"
)

// Opt-in OSC 8 hyperlinks in the output pane (task 110).
//
// Task 075 decision 3 kept every link a numbered reference and emitted no
// hyperlink escape, for three reasons this file answers one by one:
//
//   - Nothing probes the terminal. `tui.hyperlinks` is off by default and the
//     human turns it on; off, the pane is byte-identical to what it was.
//   - The payload is an agent-supplied URL inside an escape sequence (§16).
//     hyperlinkTarget below is the only door to a link, and a destination it
//     refuses renders exactly as it did before. The reference block stays on
//     screen either way, so the true destination is always visible as text —
//     which is the defence against a label that reads as one URL and opens
//     another.
//   - A link across a wrap boundary would have to be closed and reopened per
//     line. The link is a style attribute (lipgloss's Hyperlink), and the pane
//     already styles each produced line's runs after layout, so every line
//     opens and closes its own link and the wrap-plain-then-style invariant
//     holds unchanged. sameStyle compares renders, and a render includes the
//     link, so a run already splits at a link boundary.

// maxHyperlinkBytes bounds a destination that may become a link. Longer ones
// still render, as text and as a reference, exactly as they did before.
const maxHyperlinkBytes = 2048

// hyperlinkTarget decides whether a Markdown destination may be emitted inside
// an OSC 8 sequence, and returns the URI to emit. It is stricter than "a URL
// that parses" on purpose (task 110 decision 4):
//
//   - at most maxHyperlinkBytes bytes;
//   - every byte printable ASCII, 0x21–0x7E, which refuses C0 and C1
//     controls, DEL, ESC, BEL, the 8-bit ST (0x9C), space and every non-ASCII
//     byte. Non-ASCII is refused rather than percent-encoded: that is what
//     blocks a look-alike Unicode host, and OSC 8 expects a URI anyway;
//   - net/url parses it;
//   - the scheme, which url.Parse lowercases, is http or https;
//   - the host is non-empty, which refuses `https:` and a path-only URL;
//   - there is no userinfo, which refuses `https://github.com@evil.example`.
//
// The URI returned is the validated original, never u.String(): the printed
// reference and the link target stay byte-identical.
func hyperlinkTarget(dest string) (string, bool) {
	if dest == "" || len(dest) > maxHyperlinkBytes {
		return "", false
	}
	for i := range len(dest) {
		if c := dest[i]; c < 0x21 || c > 0x7e {
			return "", false
		}
	}
	u, err := url.Parse(dest)
	if err != nil {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	if u.Host == "" || u.User != nil {
		return "", false
	}
	return dest, true
}

// hyperlinkDocID is a rendered document's identity, for link ids. It is a
// digest of the source rather than anything the source says, so no agent
// byte reaches the id: hex is inside OSC 8's safe set by construction.
func hyperlinkDocID(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// hyperlinkID names one link of one document. Every piece of that link — a
// label wrapped across lines, its `[n]`, its reference line — carries the
// same id, which is what lets a terminal treat them as one link on hover.
// Only [A-Za-z0-9-] ever appears in it.
func hyperlinkID(doc string, n int) string {
	return "vincent-" + doc + "-" + strconv.Itoa(n)
}

// hyperlinkHolder is the session's `tui.hyperlinks` value, shared by the task
// workspace and the chat workspace the way levelHolder and rawHolder are, so
// the two panes never disagree. Unlike those two it is configuration rather
// than a toggle: the root fills it from every GET /v1/config and from the
// config editor's save, and no key changes it.
type hyperlinkHolder struct{ on bool }

func newHyperlinkHolder() *hyperlinkHolder { return &hyperlinkHolder{} }

func (h *hyperlinkHolder) get() bool { return h.on }

func (h *hyperlinkHolder) set(on bool) { h.on = on }

// separatorStyle is the style a held word separator is drawn in. It is the
// next word's style, as it always was, except when that word opens a link
// the run before it is not part of: the space in front of a link is prose,
// and a terminal that underlines a link on hover would otherwise underline
// the gap too. With hyperlinks off no style carries a link, so this returns
// next and the pane's bytes do not change.
func separatorStyle(prev, next lipgloss.Style) lipgloss.Style {
	link, _ := next.GetHyperlink()
	if link == "" {
		return next
	}
	if prevLink, _ := prev.GetHyperlink(); prevLink == link {
		return next
	}
	return next.UnsetHyperlink()
}
