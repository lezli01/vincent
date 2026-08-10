package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The §15 footer: one line, never wraps. Left to right: the focused
// surface's keys (at most five, in registry priority order), then the
// selected task's available_actions, then — pinned right and never
// truncated — `: commands  ? help  q quit`. Overflow truncates from the
// left with `…`: the pinned segment is the escape hatch that makes every
// other key optional, so a narrow terminal dropping it would fail exactly
// when the human is most lost.

// maxFooterHints caps the focused surface's key segment.
const maxFooterHints = 5

// renderFooter composes the line. bar is the shell's action bar (nil on a
// takeover); a pending confirmation replaces the left segments outright —
// it owns the keyboard, so nothing else is actionable anyway. attention is
// the needs-a-human count behind the `!` hint, shown only when non-zero;
// retry adds the reconnect hint while the daemon is unreachable.
func renderFooter(width int, panelRows []binding, bar *actionBar, target taskActions, attention int, retry bool) string {
	pinned := styleKey.Render(":") + styleDim.Render(" commands  ") +
		styleKey.Render("?") + styleDim.Render(" help  ") +
		styleKey.Render("q") + styleDim.Render(" quit")

	var left string
	if bar != nil && bar.capturing() {
		left = strings.TrimPrefix(bar.render(target), " ")
	} else {
		sep := styleDim.Render(" · ")
		segs := make([]string, 0, 4)
		if hints := footerHints(panelRows); len(hints) > 0 {
			segs = append(segs, strings.Join(hints, sep))
		}
		if bar != nil && target.id != 0 {
			var extra []string
			if target.has(apiclient.ActionAnswer) {
				extra = append(extra, styleAsk.Render("enter answer"))
			}
			if actions := strings.TrimPrefix(bar.render(target, extra...), " "); actions != "" {
				segs = append(segs, actions)
			}
		}
		if attention > 0 {
			segs = append(segs, styleWarn.Render(fmt.Sprintf("! next attention (%d)", attention)))
		}
		if retry {
			segs = append(segs, styleKey.Render("r")+" retry")
		}
		left = strings.Join(segs, sep)
	}

	line := " " + left
	if width <= 0 {
		return line + "  " + pinned
	}
	pw := ansi.StringWidth(pinned)
	avail := width - pw - 2
	if avail <= 0 {
		// Below any §15 floor: the pinned segment alone, never truncated.
		return " " + pinned
	}
	lw := ansi.StringWidth(line)
	if lw > avail {
		line = "…" + ansi.TruncateLeft(line, lw-avail+1, "")
		lw = ansi.StringWidth(line)
	}
	return line + strings.Repeat(" ", max(width-lw-pw, 2)) + pinned
}

// footerHints renders the surface's footer-worthy keys: rows with a hint,
// in priority order, at most five.
func footerHints(rows []binding) []string {
	hinted := make([]binding, 0, len(rows))
	for _, r := range rows {
		if r.hint != "" {
			hinted = append(hinted, r)
		}
	}
	sort.SliceStable(hinted, func(i, j int) bool { return hinted[i].priority < hinted[j].priority })
	if len(hinted) > maxFooterHints {
		hinted = hinted[:maxFooterHints]
	}
	out := make([]string, 0, len(hinted))
	for _, r := range hinted {
		key, rest, ok := strings.Cut(r.hint, " ")
		if !ok {
			out = append(out, styleKey.Render(r.hint))
			continue
		}
		out = append(out, styleKey.Render(key)+" "+styleDim.Render(rest))
	}
	return out
}
