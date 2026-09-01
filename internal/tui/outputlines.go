package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// cols is a string's printable width in terminal cells. Every measurement in
// this file goes through it: the gutters are glyphs, so `·` is one column and
// three bytes, and byte-wise column math would indent every wrapped reasoning
// line wrong.
//
// Cells rather than runes (task 073 decision 2). A rune count is right for
// ASCII and wrong for everything an agent actually prints: a CJK glyph or an
// emoji occupies two columns, a combining mark occupies none, and a ZWJ
// sequence is one grapheme of several runes. ansi.StringWidth is what
// wrapCellLines and the rest of the TUI already measure with, so there is now
// one answer to "how wide is this" in the package.
//
// This does not make the wrapping ANSI-aware, and it is not meant to: §15's
// invariant stands — text is wrapped while plain and each produced line is
// styled afterwards, so no break can land inside an escape sequence.
func cols(s string) int { return ansi.StringWidth(s) }

// sanitizeText removes everything in agent-supplied text that would drive the
// terminal rather than fill it (task 073 decision 7).
//
// Every record goes through it, not just the Markdown path: agent text
// reached the pane unfiltered before, so a tool result summary or a command's
// output body carrying `\x1b[2J` could already clear the screen, and an OSC
// sequence could already rewrite the window title. Doing it here — at
// wrapLine's segment emission — covers every record type at one site and
// cannot be bypassed by a record type added later. It also runs before any
// measurement, so an escape sequence can never influence the width
// arithmetic either.
//
// Newlines and tabs survive: the first is the pane's own paragraph break and
// the second is indentation a fenced code block must keep.
func sanitizeText(s string) string {
	if strings.IndexFunc(s, isTerminalControl) < 0 {
		return s
	}
	// ansi.Strip removes whole escape sequences, parameters included, which
	// dropping the ESC alone would not: it would leave `[2J` as text.
	s = ansi.Strip(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isTerminalControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isTerminalControl reports the C0 and C1 controls and DEL, minus the two
// whitespace characters the pane renders itself.
func isTerminalControl(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

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

// levelHolder is the one verbosity level the whole session is on. The task
// workspace's output pane and the chat workspace share it by pointer rather
// than each keeping their own (task 071 decision 3): §15's reason for the
// level being session state — moving around should not reset what a reader
// chose to see — does not stop at a view boundary. Nothing persists it; it
// dies with the process, exactly as §15 already reasons for the task pane.
type levelHolder struct{ level outputLevel }

func newLevelHolder() *levelHolder { return &levelHolder{level: levelNormal} }

func (h *levelHolder) get() outputLevel { return h.level }

func (h *levelHolder) set(l outputLevel) { h.level = l }

// cycle advances the level.
func (h *levelHolder) cycle() { h.level = h.level.next() }

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
	// contPrefix replaces the spaces a continuation is otherwise indented
	// with. A blockquote's bar has to appear on every line of the quote: a
	// marker drawn once and then dropped is a content gutter that is not a
	// gutter (task 073 decision 6). Empty means the spaces, which is every
	// caller that predates Markdown.
	contPrefix string
	// pre marks preformatted content — a fenced code block's line — which
	// hard-wraps at the cell boundary and keeps every space and tab instead
	// of going through splitWords, whose strings.Fields would collapse the
	// indentation the block exists to show.
	pre bool
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
	// gutterPlan marks the agent's running to-do list (task 070). Two
	// columns like the rest, and its own glyph because a plan is neither
	// something the agent said nor something it ran.
	gutterPlan = "☰ "
)

// wrapLine lays a paneLine out across a pane of the given width, styling each
// produced line's pieces separately so an escape sequence is never split by a
// break. Words longer than the available width are hard-split rather than
// allowed to overflow — a base64 blob or a path with no spaces would
// otherwise reintroduce the clipping this replaces.
func wrapLine(pl paneLine, width int) []string {
	if pl.pre {
		return wrapPre(pl, width)
	}
	gutterWidth := cols(pl.gutter)
	avail := width - gutterWidth
	if avail < 8 {
		// Too narrow to lay anything out; the viewport clips, which at this
		// width is all that is left.
		avail = 8
	}
	indent := continuationIndent(pl, gutterWidth)

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
		for i, para := range strings.Split(sanitizeText(seg.text), "\n") {
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

// continuationIndent is what a wrapped line starts with. It is the gutter's
// width in spaces unless the caller asked for a prefix, which is padded or
// cut to exactly that width so the column arithmetic above still holds.
func continuationIndent(pl paneLine, gutterWidth int) string {
	if pl.contPrefix == "" {
		return strings.Repeat(" ", gutterWidth)
	}
	return pl.gutterStyle.Render(padCells(pl.contPrefix, gutterWidth))
}

// padCells makes a string exactly width cells wide.
func padCells(s string, width int) string {
	switch n := cols(s); {
	case n == width:
		return s
	case n > width:
		return ansi.Cut(s, 0, width)
	default:
		return s + strings.Repeat(" ", width-n)
	}
}

// wrapPre lays a preformatted line out: no word breaking, no collapsing of
// whitespace, no ellipsis. A code line longer than the pane continues on the
// next line at the block's own rail (task 073 decision 3), which is what the
// pane already does for a long path — truncating would put the tail of a
// long line out of reach of the TUI entirely, and clipping is what T4.16
// removed.
// A preformatted line may carry several segments (task 075): a highlighted
// code line is one run per token and a reference line is its number plus its
// destination. They are laid out as one continuous stream of cells — the run
// boundaries are styling, not layout — so a wrap lands wherever the cell
// boundary falls, exactly as it did when the line was a single run.
func wrapPre(pl paneLine, width int) []string {
	gutterWidth := cols(pl.gutter)
	avail := width - gutterWidth
	if avail < 8 {
		avail = 8
	}
	head := pl.gutterStyle.Render(pl.gutter)
	cont := head
	if pl.contPrefix != "" {
		cont = continuationIndent(pl, gutterWidth)
	}

	var (
		out []string
		cur strings.Builder
	)
	col := 0
	flush := func() {
		prefix := cont
		if len(out) == 0 {
			prefix = head
		}
		out = append(out, prefix+cur.String())
		cur.Reset()
		col = 0
	}
	for _, seg := range pl.segs {
		// Tabs are expanded to the four columns lipgloss renders them as.
		// Measuring the tab and rendering the spaces would disagree —
		// ansi.StringWidth calls a tab zero columns wide — and a
		// tab-indented code block would then overflow the pane by exactly
		// its indentation.
		text := strings.ReplaceAll(sanitizeText(seg.text), "\t", "    ")
		for i, src := range strings.Split(text, "\n") {
			if i > 0 {
				flush()
			}
			for src != "" {
				room := avail - col
				if room <= 0 {
					flush()
					room = avail
				}
				chunk := src
				if cols(src) > room {
					chunk = ansi.Cut(src, 0, room)
				}
				if chunk == "" {
					// A single grapheme wider than the room left. Take the
					// next line if there is one to take, and otherwise emit
					// it whole rather than spin, since cutting it produces
					// nothing.
					if col > 0 {
						flush()
						continue
					}
					chunk = src
				}
				cur.WriteString(seg.style.Render(chunk))
				col += cols(chunk)
				rest := ansi.TruncateLeft(src, cols(chunk), "")
				if rest == src {
					break
				}
				src = rest
			}
		}
	}
	flush()
	return out
}

// splitWords breaks a paragraph into words and single-space separators,
// preserving a leading space so a segment that begins with one — a tool
// call's " subject" following its name — is not silently joined to the
// segment before it, and a trailing one so a segment that ends in a space —
// the prose before an inline `code` span — is not joined to the segment
// after it. A held separator is dropped at a line break, so preserving it
// cannot leave trailing whitespace on a rendered line.
//
// Over-long words are hard-split rather than allowed to overflow: a path or a
// base64 blob with no spaces would otherwise reintroduce the clipping this
// wrapping replaces. The split is by cell, not by rune (task 073 decision 2):
// slicing a rune index cuts a ZWJ emoji into its parts and mis-measures every
// wide glyph, for the same reason a rune count did.
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
		for cols(field) > avail {
			head := ansi.Cut(field, 0, avail)
			rest := ansi.TruncateLeft(field, cols(head), "")
			if head == "" || rest == "" || rest == field {
				break
			}
			out = append(out, head, " ")
			field = rest
		}
		out = append(out, field)
	}
	if len(fields) > 0 && strings.HasSuffix(para, " ") {
		out = append(out, " ")
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

// planLine renders the agent's running to-do list — the plan it wrote for
// itself, ticked over as it works (task 070). Every version of the list
// arrives whole, so this renders the current state rather than a diff: a
// reader who joins mid-run wants to know where the agent is, not how it got
// there.
//
// Done items are dimmed and pending ones are not, so the list scans to the
// line the agent is on. It is one paneLine, which the pane wraps to the
// hanging indent under gutterPlan rather than clipping.
func planLine(items []apiclient.TranscriptPlanItem) paneLine {
	segs := make([]segment, 0, len(items)*2)
	for i, item := range items {
		if i > 0 {
			segs = append(segs, segment{text: " · ", style: styleDim})
		}
		mark, style := "○ ", lipgloss.NewStyle()
		if item.Completed {
			mark, style = "✓ ", styleOKDim
		}
		segs = append(segs, segment{text: mark + item.Text, style: style})
	}
	return paneLine{gutter: gutterPlan, gutterStyle: styleDim, segs: segs}
}

// commandOutputLine renders what a command printed. It is dim and gutterless
// like the output of a command step, because it is the same thing: the body
// a tool wrote, not vincent's account of it. Truncation is stated rather than
// silent — output that stops mid-line and says nothing is indistinguishable
// from a command that printed exactly that much (task 070 decision 2).
func commandOutputLine(rec apiclient.TranscriptRecord) paneLine {
	text := strings.TrimRight(rec.Output, "\n")
	if rec.Truncated {
		text += "\n… output truncated"
	}
	return paneLine{
		gutter:      gutterNone,
		gutterStyle: styleDim,
		segs:        []segment{{text: text, style: styleDim}},
	}
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
func thinkingBlock(text string, level outputLevel, width int, expandKey string) []string {
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
		gutterNone+styleDim.Render(fmt.Sprintf("… +%d lines (%s)", hidden, expandKey)))
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

// lineOpts are the pane-specific parts of a render: what to say when records
// were dropped, and the label of the key that expands what a level collapsed.
// The key differs between the two panes — `v` in the task workspace, `ctrl+r`
// in a chat, where every letter belongs to the composer — and a hint naming a
// key that does something else where it is read is worse than no hint.
type lineOpts struct {
	expandKey     string
	truncatedNote string
}

// outputLines renders the normalized records into wrapped pane lines.
//
// Two rules shape the result beyond the per-record rendering. Consecutive
// unrecognized lines collapse into a count — a dialect vincent does not model
// must not be able to drown the output a human is reading — and `v` expands
// them rather than leaving the count a dead end. And an assistant message
// that follows anything else gets a blank line before it, which is what
// separates one turn from the next without spending a column on it.
func outputLines(records []apiclient.TranscriptRecord, level outputLevel, width int, opts lineOpts) []string {
	lines := make([]string, 0, len(records)+1)
	if opts.truncatedNote != "" {
		lines = append(lines, styleDim.Render(gutterNone+opts.truncatedNote))
	}
	// sawOutput drives the T4.16 result de-duplication: every dialect's
	// result text repeats assistant messages already on screen — cursor's is
	// the whole turn concatenated — so the final record shows its outcome
	// alone, unless nothing else ever rendered.
	var sawOutput, lastWasOutput bool
	rawRun := 0
	flushRaw := func() {
		if rawRun == 0 {
			return
		}
		lines = append(lines, styleDim.Render(fmt.Sprintf(
			"%s… %d unrecognized line(s) (%s)", gutterNone, rawRun, opts.expandKey)))
		rawRun = 0
	}
	for _, rec := range records {
		if rec.Type == "agent.raw" {
			if level == levelVerbose {
				flushRaw()
				lines = append(lines, wrapLine(paneLine{
					gutter:      gutterNone,
					gutterStyle: styleDim,
					segs:        []segment{{text: rec.Line, style: styleDim}},
				}, width)...)
				lastWasOutput = false
				continue
			}
			rawRun++
			continue
		}
		flushRaw()
		if rec.Type == "agent.output" {
			// Assistant prose is the one record type that is Markdown
			// (task 073 decision 5), and it is handled here rather than in
			// renderRecord because one record now yields several pane lines
			// — the same reason agent.thinking is handled above.
			block := markdownLines(rec.Text, width)
			if len(block) == 0 {
				continue
			}
			if !lastWasOutput && len(lines) > 0 {
				lines = append(lines, "")
			}
			sawOutput = true
			lines = append(lines, block...)
			lastWasOutput = true
			continue
		}
		if rec.Type == "agent.thinking" {
			if block := thinkingBlock(rec.Text, level, width, opts.expandKey); len(block) > 0 {
				lines = append(lines, block...)
				lastWasOutput = false
			}
			continue
		}
		pl, ok := renderRecord(rec, sawOutput, level)
		if !ok {
			continue
		}
		if pl.isOutput {
			if !lastWasOutput && len(lines) > 0 {
				lines = append(lines, "")
			}
			sawOutput = true
		}
		lines = append(lines, wrapLine(pl, width)...)
		lastWasOutput = pl.isOutput
	}
	flushRaw()
	return lines
}

// renderRecord maps one normalized record to a pane line. A record with
// nothing a reader wants mid-tail reports ok=false: agent.usage is the whole
// point of that rule, since the timeline row already carries its numbers —
// though levelVerbose does show it, adapter-native payload and all, because
// that level means "show me the machine".
func renderRecord(rec apiclient.TranscriptRecord, sawOutput bool, level outputLevel) (paneLine, bool) {
	switch rec.Type {
	case "agent.tool_use":
		if len(rec.Tools) == 0 {
			return paneLine{}, false
		}
		return toolUsePane(rec.Tools), true
	case "agent.tool_result":
		if len(rec.Results) == 0 {
			return paneLine{}, false
		}
		// One record can report several outcomes; the first owns the line
		// and the rest are rare enough to share it rather than earn rows.
		return toolResultLine(rec.Results[0]), true
	case "agent.run_header":
		// levelCompact is "what the agent said and did, nothing else"; the
		// run header is neither, so it appears from normal up (task 066).
		if level == levelCompact {
			return paneLine{}, false
		}
		return runHeaderLine(rec), true
	case "agent.plan":
		// levelCompact is "what the agent said and did, nothing else". A
		// plan is neither — it is what the agent intends — so it appears
		// from normal up, where the run header does (task 070).
		if level == levelCompact || len(rec.Items) == 0 {
			return paneLine{}, false
		}
		return planLine(rec.Items), true
	case "agent.command_output":
		// Verbose only. This is the output body, and a step that runs
		// `go test ./...` would otherwise flood the level most readers use
		// (task 070 decision 2).
		if level != levelVerbose || rec.Output == "" {
			return paneLine{}, false
		}
		return commandOutputLine(rec), true
	case "agent.usage":
		if level != levelVerbose {
			return paneLine{}, false
		}
		return plain(string(rec.Raw), styleDim, false), len(rec.Raw) > 0
	case "agent.error":
		return marked("✗ ", rec.Message, styleBad), true
	case "agent.result":
		return renderResult(rec, sawOutput, level), true
	case "command.output", "vincent.output":
		if rec.Stream == "stderr" {
			return plain(rec.Text, styleStderr, false), true
		}
		return plain(rec.Text, lipgloss.NewStyle(), false), true
	case "vincent.command_started":
		return marked("$ ", fieldOf(rec.Raw, "command"), styleDim), true
	case "vincent.input_request":
		return marked("? ", firstNonEmpty(rec.Summary, rec.Kind, "input requested"), styleAsk), true
	case "vincent.input_response":
		return marked("✓ ", "answered", styleAsk), true
	case "vincent.input_timeout", "vincent.input_protocol_error", "vincent.error":
		return marked("✗ ",
			firstNonEmpty(rec.Message, fieldOf(rec.Raw, "error"), rec.Type), styleBad), true
	default:
		if strings.HasPrefix(rec.Type, "vincent.") {
			return marked("· ", strings.TrimPrefix(rec.Type, "vincent."), styleDim), true
		}
		return paneLine{}, false
	}
}
