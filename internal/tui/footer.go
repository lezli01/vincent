package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The §15 footer: one line, never wraps. Left to right: the focused
// surface's keys — as many as the width holds, in registry priority order —
// then the selected task's available_actions, then `+N`: how many of this
// surface's palette-reachable keys the line is not showing. Pinned right and
// never truncated: `: commands  ? help  q quit`. Overflow truncates from the
// left with `…`: the pinned segment is the escape hatch that makes every
// other key optional, so a narrow terminal dropping it would fail exactly
// when the human is most lost.
//
// The five-key cap the phase 3 refactor and the PR R / T3.12 decisions fixed
// here is superseded by task 094 (spec §15, amended 2026-09-10; decision
// record row 32). What those decisions were protecting — one line that never
// wraps and never truncates the pinned escape hatch — is unchanged. Five was
// the mechanism, and it was wrong at both 80 and 200 columns.

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
	// counts marks a segment standing for something `+N` would otherwise
	// report as hidden — a registry row the palette lists, or a task action.
	// Losing one to the `…` puts it back into the count (task 094).
	counts bool
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
	var pinned strings.Builder
	for _, s := range pinnedSegs {
		pinned.WriteString(s.text)
	}
	pw := ansi.StringWidth(pinned.String())

	// The pending y/n owns the keyboard and the left of the line: nothing
	// else is actionable, so nothing else — `+N` included — is advertised.
	capturing := bar != nil && bar.capturing()
	var hints, rest []footerSeg
	if capturing {
		rest = []footerSeg{{text: strings.TrimPrefix(bar.render(target), " ")}}
	} else {
		hints = footerHintSegs(panelRows)
		rest = footerRestSegs(bar, target, attention, retry)
	}

	if width <= 0 {
		// No width to budget against and nothing to truncate, so nothing is
		// hidden by a layout that was never laid out: every hint, no `+N`.
		line, _ := composeFooter(append(hints, rest...))
		return line + "  " + pinned.String(), nil
	}
	avail := width - pw - 2
	if avail <= 0 {
		// Below any §15 floor: the pinned segment alone, never truncated.
		return " " + pinned.String(), pinnedHits(1, pinnedSegs)
	}

	// What `+N` counts against: this surface's palette-reachable rows, plus
	// the task actions on the line — each of those is one more thing the
	// palette can reach that the `…` may take away. A confirmation counts
	// nothing: it replaced the left side, and nothing else is actionable.
	countable, pool := 0, 0
	if !capturing {
		countable = footerCountable(panelRows)
		pool = countable
		for _, s := range rest {
			if s.counts {
				pool++
			}
		}
	}

	k := footerAdmit(hints, rest, countable, avail)
	segs := make([]footerSeg, 0, k+len(rest)+1)
	segs = append(segs, hints[:k]...)
	segs = append(segs, rest...)
	shown := 0
	for _, s := range segs {
		if s.counts {
			shown++
		}
	}

	// `+N` is composed against the count it reports, and that count grows
	// with whatever the `…` removed — which depends on the `+N`'s own width.
	// It is 0, 2 or 3 cells, and a pass can only widen it, so the two settle
	// within three passes; the loop is bounded rather than trusted.
	var (
		line string
		hits []footerHit
	)
	n := pool - shown
	for range 4 {
		l, h, lost := footerFit(segs, n, avail)
		line, hits = l, h
		if pool-shown+lost == n {
			break
		}
		n = pool - shown + lost
	}

	lw := ansi.StringWidth(line)
	pad := max(width-lw-pw, 2)
	hits = append(hits, pinnedHits(lw+pad, pinnedSegs)...)
	return line + strings.Repeat(" ", pad) + pinned.String(), hits
}

// footerSep joins two segments. Its width is the per-gap cost every budget
// here has to pay for.
func footerSep() (string, int) {
	sep := styleDim.Render(" · ")
	return sep, ansi.StringWidth(sep)
}

// composeFooter joins the segments into the left side of the line and
// reports the column each one starts at.
func composeFooter(segs []footerSeg) (string, []int) {
	sep, sepW := footerSep()
	var sb strings.Builder
	sb.WriteString(" ")
	x := 1
	at := make([]int, len(segs))
	for i, s := range segs {
		if i > 0 {
			sb.WriteString(sep)
			x += sepW
		}
		at[i] = x
		sb.WriteString(s.text)
		x += ansi.StringWidth(s.text)
	}
	return sb.String(), at
}

// footerFit composes segs with a `+N` for n, truncates from the left to
// avail, and reports the surviving spans plus how many counting segments the
// `…` removed. A span whose start was cut cannot be clicked — the text that
// remains of it is a fragment, and clicking a fragment would fire a key the
// reader cannot see.
func footerFit(segs []footerSeg, n, avail int) (string, []footerHit, int) {
	all := segs
	if n > 0 {
		all = make([]footerSeg, 0, len(segs)+1)
		all = append(all, segs...)
		all = append(all, footerMoreSeg(n))
	}
	line, at := composeFooter(all)
	shift := 0
	if lw := ansi.StringWidth(line); lw > avail {
		cut := lw - avail + 1
		line = "…" + ansi.TruncateLeft(line, cut, "")
		shift = cut - 1
	}
	hits := make([]footerHit, 0, len(all)+3)
	lost := 0
	for i, s := range all {
		x0 := at[i] - shift
		if x0 < 1 {
			if s.counts {
				lost++
			}
			continue
		}
		if s.key != "" {
			hits = append(hits, footerHit{x0: x0, x1: x0 + ansi.StringWidth(s.text), key: s.key})
		}
	}
	return line, hits, lost
}

// footerAdmit is §15's width contest (task 094 decision 1): the largest
// prefix of the priority-ordered hint segments whose whole composed line —
// the hints, everything that follows them, and a `+N` sized for the count
// that admission implies — still fits in avail. Every candidate is measured
// against its own N, so there is no "admitted because +9 shrank to +8" state
// left to detect afterwards. It is a prefix rather than a best fit because
// priority means priority: a wide row is never skipped to squeeze in a
// narrow one behind it. A context declares at most a dozen rows, so the
// sweep costs nothing.
//
// The segments that follow the hints are measured first and come out of the
// budget (decision 2). The line truncates from the left, so hints are what it
// loses first; admitting them against the whole width would let the actions,
// `!`, `r retry` and the status push the just-admitted hints straight back
// off the line.
func footerAdmit(hints, rest []footerSeg, countable, avail int) int {
	_, sepW := footerSep()
	restW := 0
	for _, s := range rest {
		restW += ansi.StringWidth(s.text)
	}
	best, sum, shown := 0, 0, 0
	for k := 0; k <= len(hints); k++ {
		if k > 0 {
			sum += ansi.StringWidth(hints[k-1].text)
			if hints[k-1].counts {
				shown++
			}
		}
		total, count := 1+sum+restW, k+len(rest)
		if n := countable - shown; n > 0 {
			total += ansi.StringWidth(footerMoreSeg(n).text)
			count++
		}
		if count > 1 {
			total += sepW * (count - 1)
		}
		if total <= avail {
			best = k
		}
	}
	return best
}

// footerMoreSeg is the `+N`: how many of this surface's keys the line is not
// showing. It is a normal segment keyed `:` — clicking it opens the palette
// those keys live in, which is the one-execution-path rule every other span
// follows, and the truncation shift gives it "a cut span cannot be clicked"
// for free. The palette is deliberately the target rather than a second
// menu: paletteEntries already lists exactly these rows from the same
// registry, and a second surface would be a second thing to keep in sync.
func footerMoreSeg(n int) footerSeg {
	return footerSeg{text: styleDim.Render(fmt.Sprintf("+%d", n)), key: ":"}
}

// footerCountable is what `+N` counts against (task 094 decision 3): the rows
// of this surface that the palette lists. Global rows are never in it — the
// pinned `: commands  ? help  q quit` is what stands for those, and counting
// them would put `N` around 14 on every board and never at zero. Neither are
// the alias rows another row's hint already advertises, or a grouped board
// would carry a permanent `+2` for `right` and `O` with nothing actually
// hidden. The rows arrive already filtered by withoutGitHub and
// shell.liveBindings, so a key that does nothing right now is neither shown
// nor counted (task 054 decision 5).
func footerCountable(rows []binding) int {
	n := 0
	for _, b := range rows {
		if !b.noPalette && !b.aliased {
			n++
		}
	}
	return n
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

// footerRestSegs is everything to the right of the hints: the task's valid
// actions, the answer/attention/retry extras, and the action bar's last
// status.
func footerRestSegs(bar *actionBar, target taskActions, attention int, retry bool) []footerSeg {
	segs := make([]footerSeg, 0, 8)
	if bar != nil && (target.id != 0 || target.bulk()) {
		for _, o := range actionOrder {
			if target.has(o.action) {
				segs = append(segs, footerSeg{
					text: styleKey.Render(o.key) + " " + actionLabel(target, o.action), key: o.key, counts: true,
				})
			}
		}
		if target.has(apiclient.ActionAnswer) {
			segs = append(segs, footerSeg{text: styleAsk.Render("enter answer"), key: "enter", counts: true})
		}
	}
	if attention > 0 {
		// `!` is a global row, and the pinned segment stands for those: shown
		// here, never counted.
		segs = append(segs, footerSeg{
			text: styleWarn.Render(fmt.Sprintf("! next attention (%d)", attention)), key: "!",
		})
	}
	if retry {
		// The reconnect hint has no registry row at all — the only `r` row is
		// the §6 retry action — so the palette cannot reach it and `+N` never
		// speaks for it.
		segs = append(segs, footerSeg{text: styleKey.Render("r") + " retry", key: "r"})
	}
	if bar != nil && bar.status != "" {
		style := styleDim
		if bar.statusBad {
			style = styleBad
		}
		// Unclickable text, and nothing the palette can reach: never counted.
		segs = append(segs, footerSeg{text: style.Render(bar.status)})
	}
	return segs
}

// footerHintSegs renders every footer-worthy key of the surface — the rows
// carrying a hint, in priority order. How many of them reach the line is
// footerAdmit's answer, not a constant's.
func footerHintSegs(rows []binding) []footerSeg {
	hinted := make([]binding, 0, len(rows))
	for _, r := range rows {
		if r.hint != "" {
			hinted = append(hinted, r)
		}
	}
	sort.SliceStable(hinted, func(i, j int) bool { return hinted[i].priority < hinted[j].priority })
	out := make([]footerSeg, 0, len(hinted))
	for _, r := range hinted {
		// A hinted row the palette does not list — the popup surfaces — is
		// shown but never counted: `+N` points at a palette that would list
		// none of these keys.
		seg := footerSeg{key: r.key, counts: !r.noPalette}
		key, rest, ok := strings.Cut(r.hint, " ")
		if !ok {
			seg.text = styleKey.Render(r.hint)
		} else {
			seg.text = styleKey.Render(key) + " " + styleDim.Render(rest)
		}
		out = append(out, seg)
	}
	return out
}
