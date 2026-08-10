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

// footerHit is one clickable span: clicking it fires the key it shows
// (§15 Mouse) — the palette's one-execution-path rule again.
type footerHit struct {
	x0, x1 int
	key    string
}

// footerSeg is one composed segment; key is empty for unclickable text
// (statuses).
type footerSeg struct {
	text string
	key  string
}

// renderFooter composes the line; buildFooter additionally reports the
// clickable spans. bar is the shell's action bar (nil on a takeover); a
// pending confirmation replaces the left segments outright — it owns the
// keyboard, so nothing else is actionable anyway. attention is the
// needs-a-human count behind the `!` hint, shown only when non-zero; retry
// adds the reconnect hint while the daemon is unreachable.
func renderFooter(width int, panelRows []binding, bar *actionBar, target taskActions, attention int, retry bool) string {
	line, _ := buildFooter(width, panelRows, bar, target, attention, retry)
	return line
}

func buildFooter(width int, panelRows []binding, bar *actionBar, target taskActions, attention int, retry bool) (string, []footerHit) {
	pinnedSegs := []footerSeg{
		{text: styleKey.Render(":") + styleDim.Render(" commands  "), key: ":"},
		{text: styleKey.Render("?") + styleDim.Render(" help  "), key: "?"},
		{text: styleKey.Render("q") + styleDim.Render(" quit"), key: "q"},
	}

	var segs []footerSeg
	if bar != nil && bar.capturing() {
		// The pending y/n owns the keyboard and the left of the line.
		segs = []footerSeg{{text: strings.TrimPrefix(bar.render(target), " ")}}
	} else {
		segs = footerLeftSegs(panelRows, bar, target, attention, retry)
	}

	sep := styleDim.Render(" · ")
	sepW := ansi.StringWidth(sep)
	var sb strings.Builder
	sb.WriteString(" ")
	x := 1
	hits := make([]footerHit, 0, len(segs)+3)
	for i, s := range segs {
		if i > 0 {
			sb.WriteString(sep)
			x += sepW
		}
		w := ansi.StringWidth(s.text)
		if s.key != "" {
			hits = append(hits, footerHit{x0: x, x1: x + w, key: s.key})
		}
		sb.WriteString(s.text)
		x += w
	}
	line := sb.String()

	var pinned strings.Builder
	for _, s := range pinnedSegs {
		pinned.WriteString(s.text)
	}
	pw := ansi.StringWidth(pinned.String())

	if width <= 0 {
		return line + "  " + pinned.String(), nil
	}
	avail := width - pw - 2
	if avail <= 0 {
		// Below any §15 floor: the pinned segment alone, never truncated.
		return " " + pinned.String(), pinnedHits(1, pinnedSegs)
	}
	lw := ansi.StringWidth(line)
	if lw > avail {
		cut := lw - avail + 1
		line = "…" + ansi.TruncateLeft(line, cut, "")
		lw = ansi.StringWidth(line)
		// Shift the spans left; a hint that was cut cannot be clicked.
		kept := hits[:0]
		for _, h := range hits {
			h.x0 -= cut - 1
			h.x1 -= cut - 1
			if h.x0 >= 1 {
				kept = append(kept, h)
			}
		}
		hits = kept
	}
	pad := max(width-lw-pw, 2)
	hits = append(hits, pinnedHits(lw+pad, pinnedSegs)...)
	return line + strings.Repeat(" ", pad) + pinned.String(), hits
}

// padBetween pins right to the right edge of width, left where it is, and at
// least two spaces between them. It never truncates: callers that can
// overflow (the contextual footer) trim their left side first.
func padBetween(left, right string, width int) string {
	pad := 2
	if width > 0 {
		pad = max(width-ansi.StringWidth(left)-ansi.StringWidth(right), 2)
	}
	return left + strings.Repeat(" ", pad) + right
}

func pinnedHits(x int, segs []footerSeg) []footerHit {
	out := make([]footerHit, 0, len(segs))
	for _, s := range segs {
		w := ansi.StringWidth(s.text)
		out = append(out, footerHit{x0: x, x1: x + w, key: s.key})
		x += w
	}
	return out
}

// footerLeftSegs builds the non-confirming left side: panel hints, the
// task's valid actions, the answer/attention/retry extras, and the action
// bar's last status.
func footerLeftSegs(panelRows []binding, bar *actionBar, target taskActions, attention int, retry bool) []footerSeg {
	segs := footerHintSegs(panelRows)
	if bar != nil && target.id != 0 {
		for _, o := range actionOrder {
			if target.has(o.action) {
				segs = append(segs, footerSeg{
					text: styleKey.Render(o.key) + " " + o.action, key: o.key,
				})
			}
		}
		if target.has(apiclient.ActionAnswer) {
			segs = append(segs, footerSeg{text: styleAsk.Render("enter answer"), key: "enter"})
		}
	}
	if attention > 0 {
		segs = append(segs, footerSeg{
			text: styleWarn.Render(fmt.Sprintf("! next attention (%d)", attention)), key: "!",
		})
	}
	if retry {
		segs = append(segs, footerSeg{text: styleKey.Render("r") + " retry", key: "r"})
	}
	if bar != nil && bar.status != "" {
		style := styleDim
		if bar.statusBad {
			style = styleBad
		}
		segs = append(segs, footerSeg{text: style.Render(bar.status)})
	}
	return segs
}

// footerHintSegs renders the surface's footer-worthy keys: rows with a
// hint, in priority order, at most five.
func footerHintSegs(rows []binding) []footerSeg {
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
	out := make([]footerSeg, 0, len(hinted))
	for _, r := range hinted {
		key, rest, ok := strings.Cut(r.hint, " ")
		if !ok {
			out = append(out, footerSeg{text: styleKey.Render(r.hint), key: r.key})
			continue
		}
		out = append(out, footerSeg{
			text: styleKey.Render(key) + " " + styleDim.Render(rest), key: r.key,
		})
	}
	return out
}
