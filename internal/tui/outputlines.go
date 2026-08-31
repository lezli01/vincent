package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// cols is a string's printable width. Every measurement in this file goes
// through it: the gutters are glyphs, so `·` is one column and three bytes,
// and byte-wise column math would indent every wrapped reasoning line wrong.
func cols(s string) int { return utf8.RuneCountInString(s) }

// The output pane's line model (T4.16).
//
// Every record renders to a two-column **gutter** plus styled content.
// Assistant prose gets a blank gutter and so sits flush against the pane's
// left edge; everything the agent *did* is marked. That is the whole scheme:
// what the agent says is unmarked, what it does is glyphed, and a monochrome
// terminal or an SSH session loses nothing, which colour alone would not
// survive.
//
// Wrapping happens here rather than via the viewport's SoftWrap because
// SoftWrap would fold a continuation to column 0, where a wrapped line of
// reasoning becomes indistinguishable from assistant text — destroying the
// distinction the gutter exists to make. Continuations get a hanging indent
// the width of their gutter instead.

// outputLevel is how much of a record the pane shows. One key cycles it
// (§15 `v`); the level is session state, so switching attempts does not
// silently reset what a reader is looking at.
type outputLevel int

const (
	// levelCompact hides reasoning: what the agent said and did, nothing
	// else. Unrecognized lines stay behind their count at every level below
	// verbose — the count is the affordance that says there is more.
	levelCompact outputLevel = iota
	// levelNormal is the default — reasoning truncated to its first lines.
	levelNormal
	// levelVerbose shows everything, including the lines vincent's parsers
	// do not model and the adapter-native usage payloads.
	levelVerbose
)

func (l outputLevel) String() string {
	switch l {
	case levelCompact:
		return "compact"
	case levelVerbose:
		return "verbose"
	default:
		return "normal"
	}
}

// next cycles compact → normal → verbose → compact.
func (l outputLevel) next() outputLevel {
	if l >= levelVerbose {
		return levelCompact
	}
	return l + 1
}

// thinkingLines is how many wrapped lines of a reasoning block levelNormal
// shows before collapsing the rest behind a count. It is counted in *display*
// lines rather than source lines because the two dialects disagree about
// source lines — claude sends paragraphs, cursor sends one coalesced run —
// and a cap that means different things per adapter is not a cap.
const thinkingLines = 3

// segment is a run of text sharing one style. A record is a list of them, so
// a tool call can render its name and its subject differently and still wrap
// as one line.
type segment struct {
	text  string
	style lipgloss.Style
}

// paneLine is one record laid out but not yet wrapped.
type paneLine struct {
	// gutter is the marker, in plain text, and gutterStyle is how to draw
	// it. It is kept unstyled so wrapLine can fold it into the first
	// segment when they share a style: rendering them separately would put
	// an escape sequence between "▸ " and the tool name, splitting a phrase
	// that reads as one. Continuations are indented by its printable width.
	gutter      string
	gutterStyle lipgloss.Style
	segs        []segment
	// isOutput marks assistant prose, which drives the blank line that
	// separates one turn from the next.
	isOutput bool
}

var (
	styleThinking = lipgloss.NewStyle().Faint(true).Italic(true)
	styleOKDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Faint(true)
	styleErrDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Faint(true)
)

// gutters. Two columns each, so content aligns whatever the record is.
const (
	gutterNone     = "  "
	gutterThinking = "· "
	gutterTool     = "▸ "
	// gutterHeader marks the run header — the frame of the run rather than
	// anything that happened inside it (task 066).
	gutterHeader = "# "
	// gutterResult is four columns: a result is indented under its call, so
	// a run of calls and outcomes reads as a tree rather than a list.
	gutterResult = "    "
)

// wrapLine lays a paneLine out across a pane of the given width, styling each
// produced line's pieces separately so an escape sequence is never split by a
// break. Words longer than the available width are hard-split rather than
// allowed to overflow — a base64 blob or a path with no spaces would
// otherwise reintroduce the clipping this replaces.
func wrapLine(pl paneLine, width int) []string {
	gutterWidth := cols(pl.gutter)
	avail := width - gutterWidth
	if avail < 8 {
		// Too narrow to lay anything out; the viewport clips, which at this
		// width is all that is left.
		avail = 8
	}
	indent := strings.Repeat(" ", gutterWidth)

	var out []string
	var cur strings.Builder
	col := 0
	pendingSpace := false

	// Words are accumulated into a run and styled once, not styled one at a
	// time. Two reasons, and the second is why it is not a micro-optimization:
	// a Render per word puts an escape sequence between every pair of words,
	// which multiplies the pane's byte count, and it means no phrase a human
	// reads ever exists as contiguous text — including for anything that
	// searches or asserts on the rendered output.
	var run strings.Builder
	runStyle := lipgloss.NewStyle()
	flushRun := func() {
		if run.Len() > 0 {
			cur.WriteString(runStyle.Render(run.String()))
			run.Reset()
		}
	}
	// pendingGutter joins the marker to the first run when they share a
	// style, so "▸ Edit" is one phrase rather than two.
	pendingGutter := pl.gutter
	flushLine := func() {
		flushRun()
		out = append(out, cur.String())
		cur.Reset()
		col = 0
		pendingSpace = false
		pendingGutter = ""
	}
	emit := func(text string, style lipgloss.Style) {
		if !sameStyle(style, runStyle) {
			flushRun()
			runStyle = style
		}
		if pendingGutter != "" {
			if sameStyle(style, pl.gutterStyle) {
				run.WriteString(pendingGutter)
			} else {
				cur.WriteString(pl.gutterStyle.Render(pendingGutter))
			}
			pendingGutter = ""
		} else if col == 0 && cur.Len() == 0 && run.Len() == 0 && len(out) > 0 {
			cur.WriteString(indent)
		}
		run.WriteString(text)
	}
	for _, seg := range pl.segs {
		// Hard breaks in the source are honored: a multi-paragraph assistant
		// message keeps its paragraphs.
		for i, para := range strings.Split(seg.text, "\n") {
			if i > 0 {
				flushLine()
			}
			for _, tok := range splitWords(para, avail) {
				if tok == " " {
					// Held, not emitted: a separator written eagerly before
					// a word that turns out not to fit leaves the line
					// ending in whitespace, which a terminal's own text
					// selection then picks up.
					pendingSpace = col > 0
					continue
				}
				need := cols(tok)
				if pendingSpace {
					need++
				}
				if col+need > avail && col > 0 {
					flushLine()
					pendingSpace = false
					need = cols(tok)
				}
				if pendingSpace {
					emit(" ", seg.style)
					pendingSpace = false
				}
				emit(tok, seg.style)
				col += need
			}
		}
	}
	if col > 0 || len(out) == 0 {
		// A record that rendered nothing still emits its gutter, so an empty
		// assistant message does not vanish without trace.
		if pendingGutter != "" {
			cur.WriteString(pl.gutterStyle.Render(pendingGutter))
		}
		flushLine()
	}
	return out
}

// sameStyle reports whether two styles render identically. Comparing the
// rendering of a probe string is the only comparison lipgloss offers, and it
// is exact for this purpose: two styles that paint a marker the same way can
// share one escape run.
func sameStyle(a, b lipgloss.Style) bool {
	const probe = "x"
	return a.Render(probe) == b.Render(probe)
}

// splitWords breaks a paragraph into words and single-space separators,
// preserving a leading space so a segment that begins with one — a tool
// call's " subject" following its name — is not silently joined to the
// segment before it. Over-long words are hard-split rather than allowed to
// overflow: a path or a base64 blob with no spaces would otherwise
// reintroduce the clipping this wrapping replaces. Widths count runes; a
// byte-wise split would cut a multi-byte rune and emit mojibake.
func splitWords(para string, avail int) []string {
	fields := strings.Fields(para)
	out := make([]string, 0, len(fields)*2)
	if len(fields) > 0 && strings.HasPrefix(para, " ") {
		out = append(out, " ")
	}
	for i, field := range fields {
		if i > 0 {
			out = append(out, " ")
		}
		runes := []rune(field)
		for len(runes) > avail {
			out = append(out, string(runes[:avail]), " ")
			runes = runes[avail:]
		}
		out = append(out, string(runes))
	}
	return out
}

// runHeaderLine renders what the CLI announced about the run before it
// started: where it ran and what it was allowed to reach (task 066). It is
// the answer to "what could this agent actually do", which the pane could not
// give at any level before.
//
// It is dim throughout, gutter included: it is context for everything below
// it rather than a thing that happened, and it must not compete with the
// first assistant line for a reader's eye.
func runHeaderLine(rec apiclient.TranscriptRecord) paneLine {
	dir := rec.WorkDir
	if dir == "" {
		// A header with no cwd still says the run began and lists its tools,
		// which is the half a reader cannot get anywhere else.
		dir = "run started"
	}
	segs := []segment{{text: dir, style: styleDim}}
	if len(rec.AvailableTools) > 0 {
		segs = append(segs, segment{
			text:  " · " + plural(len(rec.AvailableTools), "tool", "tools") + ": " + strings.Join(rec.AvailableTools, ", "),
			style: styleDim,
		})
	}
	return paneLine{gutter: gutterHeader, gutterStyle: styleDim, segs: segs}
}

// toolResultLine renders one outcome under its call.
func toolResultLine(r apiclient.TranscriptToolResult) paneLine {
	mark, style := "✓ ", styleOKDim
	switch {
	case r.Blocked:
		// A call a permission rule refused never ran at all. It gets its own
		// mark because "the tool failed" and "the tool was not allowed" send
		// a reader to two different places — one is the agent's problem, the
		// other is the step's permission mode (task 066).
		mark, style = "⊘ ", styleErrDim
	case r.IsError:
		mark, style = "✗ ", styleErrDim
	}
	text := r.Summary
	if r.Verb != "" {
		// The verb leads: it is the dialect's own structured account of what
		// happened, where the summary is the tool's prose about it.
		text = r.Verb
		if r.Summary != "" {
			text += " · " + r.Summary
		}
	}
	if text == "" {
		// An outcome with nothing to say still says whether it worked, which
		// is the question a reader is asking.
		switch {
		case r.Blocked:
			text = "blocked"
		case r.IsError:
			text = "failed"
		default:
			text = "done"
		}
	}
	return paneLine{
		gutter:      gutterResult + mark,
		gutterStyle: style,
		segs:        []segment{{text: text, style: style}},
	}
}

// thinkingBlock renders a reasoning block at the given level: hidden at
// compact, truncated at normal, whole at verbose. Truncation is applied
// after wrapping, so "3 lines" means three lines of the pane.
func thinkingBlock(text string, level outputLevel, width int) []string {
	if level == levelCompact || text == "" {
		return nil
	}
	lines := wrapLine(paneLine{
		gutter:      gutterThinking,
		gutterStyle: styleThinking,
		segs:        []segment{{text: text, style: styleThinking}},
	}, width)
	if level == levelVerbose || len(lines) <= thinkingLines {
		return lines
	}
	hidden := len(lines) - thinkingLines
	return append(lines[:thinkingLines:thinkingLines],
		gutterNone+styleDim.Render(fmt.Sprintf("… +%d lines (v)", hidden)))
}

// formatAgentDuration renders a duration the agent itself reported. Under a
// minute it keeps a decimal, because most agent runs are seconds long and
// "0s" for a 400 ms turn is worse than no number at all; above that it hands
// over to the board's own formatter so one duration does not spell two ways.
func formatAgentDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return formatElapsed(d)
}
