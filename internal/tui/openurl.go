package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The browser opener (task 052.6). Every screen that shows a pull request
// needs a way to reach it, and the only thing vincent can do about that is
// hand the URL to whatever the platform already uses to open one.
//
// Unlike clipboard.go this fails **visibly**. Reading a clipboard silently
// falls back to the terminal's own paste, so a failure there costs nothing;
// here a human pressed a key expecting a browser, and silence is
// indistinguishable from a browser that opened on another desktop.

// openTimeout bounds the helper process. The helpers all hand off to a
// running browser and return immediately; one that has not returned in this
// long is not going to.
const openTimeout = 10 * time.Second

// errNoOpener is what a platform's helper reports when it is not installed —
// a headless Linux box with no xdg-open is the usual case.
var errNoOpener = errors.New("no browser opener found")

// openedURLMsg reports what came of an open. It carries the URL so the note
// can name it: a failure a human can copy out of is one they can still act
// on.
type openedURLMsg struct {
	url string
	err error
}

// openURL is the platform hand-off, indirected through a variable so tests
// can assert what would have been opened without launching a browser.
var openURL = openURLPlatform

// openURLCmd validates the URL and hands it over.
//
// Only http and https are opened. The URLs the TUI opens are built by the
// daemon or by this package, but the platform helpers will happily launch a
// registered handler for any scheme, and a URL is the one thing on these
// screens that originated outside vincent — refusing everything else costs
// nothing and closes that door.
func openURLCmd(raw string) tea.Cmd {
	raw = strings.TrimSpace(raw)
	return func() tea.Msg {
		if raw == "" {
			return openedURLMsg{err: errors.New("nothing to open")}
		}
		u, err := url.Parse(raw)
		if err != nil {
			return openedURLMsg{url: raw, err: fmt.Errorf("not a URL: %w", err)}
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return openedURLMsg{url: raw, err: fmt.Errorf("refusing to open a %q URL", u.Scheme)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
		defer cancel()
		return openedURLMsg{url: raw, err: openURL(ctx, raw)}
	}
}

// openFailure is the sentence a view puts on screen when an open failed.
func openFailure(msg openedURLMsg) string {
	if msg.url == "" {
		return "could not open a browser: " + errString(msg.err)
	}
	return "could not open " + msg.url + ": " + errString(msg.err)
}

// openerError prefers the helper's own message over the exit status, which
// on its own says nothing a human can act on.
func openerError(err error, out []byte) error {
	if text := trimOutput(out); text != "" {
		return errors.New(text)
	}
	return err
}

// trimOutput is the first line a helper printed, collapsed to one sentence:
// the note is a single line of footer, not a place to spill stderr.
func trimOutput(out []byte) string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return ""
	}
	if line, _, ok := strings.Cut(text, "\n"); ok {
		text = strings.TrimSpace(line)
	}
	return strings.TrimSpace(strings.TrimSuffix(text, "\r"))
}
