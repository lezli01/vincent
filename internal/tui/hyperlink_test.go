package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Opt-in OSC 8 hyperlinks (task 110). The sanitizer is asserted directly,
// because the pane's sanitizeText strips some hostile bytes before the
// renderer ever sees them and the sanitizer has to hold even if that ordering
// changes; the rendering is asserted by parsing the OSC 8 spans back out of
// produced lines.

const (
	osc8Open  = "\x1b]8;"
	osc8Close = "\x1b]8;;\x07"
)

// linkSpan is one opened-and-closed OSC 8 hyperlink in one rendered line.
type linkSpan struct {
	id, uri, text string
}

// linkSpans parses the hyperlinks out of one rendered line, failing the test
// when a line opens a link it does not close — which is the per-line
// open/close property wrapping has to keep.
func linkSpans(t *testing.T, line string) []linkSpan {
	t.Helper()
	var spans []linkSpan
	rest := line
	for {
		at := strings.Index(rest, osc8Open)
		if at < 0 {
			return spans
		}
		rest = rest[at+len(osc8Open):]
		end := strings.IndexByte(rest, '\x07')
		if end < 0 {
			t.Fatalf("an OSC 8 sequence is not terminated: %q", line)
		}
		params, uri, _ := strings.Cut(rest[:end], ";")
		rest = rest[end+1:]
		if params == "" && uri == "" {
			t.Fatalf("a link close with no open: %q", line)
		}
		closeAt := strings.Index(rest, osc8Close)
		if closeAt < 0 {
			t.Fatalf("a line leaves a link open: %q", line)
		}
		if strings.Contains(rest[:closeAt], osc8Open) {
			t.Fatalf("a link opens inside another: %q", line)
		}
		spans = append(spans, linkSpan{
			id:   strings.TrimPrefix(params, "id="),
			uri:  uri,
			text: ansi.Strip(rest[:closeAt]),
		})
		rest = rest[closeAt+len(osc8Close):]
	}
}

func linkedLines(text string, width int) []string {
	lines, _ := markdownBlockLinesLinked(text, width, true)
	return lines
}

func joinStripped(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return strings.Join(out, "\n")
}

var linkIDShape = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func TestHyperlinkTargetAcceptsPlainHTTPURLs(t *testing.T) {
	for _, dest := range []string{
		"https://example.com/x",
		"http://example.com",
		"HTTPS://example.com/upper-scheme",
		"https://example.com/a;b?c=d;e#f",
		"https://en.wikipedia.org/wiki/Go_(programming_language)",
		"https://example.com/" + strings.Repeat("a", maxHyperlinkBytes-len("https://example.com/")),
	} {
		uri, ok := hyperlinkTarget(dest)
		if !ok {
			t.Errorf("refused %q", dest)
			continue
		}
		if uri != dest {
			t.Errorf("the URI is not the validated original: %q became %q", dest, uri)
		}
	}
}

func TestHyperlinkTargetRefusesHostileDestinations(t *testing.T) {
	cases := map[string]string{
		"javascript":           "javascript:alert(1)",
		"uppercase javascript": "JAVASCRIPT:alert(1)",
		"file":                 "file:///etc/passwd",
		"data":                 "data:text/html,x",
		"ftp":                  "ftp://example.com/x",
		"scheme-relative":      "//evil.example/x",
		"empty host":           "https:",
		"empty host with path": "https:///path",
		"userinfo":             "https://github.com@evil.example",
		"encoded at in host":   "https://github.com%40evil.example",
		"ESC":                  "https://example.com/\x1b]8;;x",
		"BEL":                  "https://example.com/\x07",
		"ST":                   "https://example.com/\x1b\\",
		"8-bit ST":             "https://example.com/\x9c",
		"NUL":                  "https://example.com/\x00",
		"C1 CSI":               "https://example.com/\x9b2J",
		"DEL":                  "https://example.com/\x7f",
		"space":                "https://example.com/a b",
		"tab":                  "https://example.com/\t",
		"cyrillic host":        "https://gіthub.com/x",
		"non-ascii path":       "https://example.com/café",
		"too long":             "https://example.com/" + strings.Repeat("a", maxHyperlinkBytes-len("https://example.com/")+1),
		"empty":                "",
		"not a url":            "https://exa mple.com",
	}
	for name, dest := range cases {
		if uri, ok := hyperlinkTarget(dest); ok {
			t.Errorf("%s: accepted %q as %q", name, dest, uri)
		}
		// linked is the renderer's only call into the sanitizer: a refusal
		// has to leave the style exactly as the setting-off path has it.
		refs := &mdRefs{links: true, doc: hyperlinkDocID("doc")}
		if got := refs.linked(styleMDRef, dest, 1); !sameStyle(got, styleMDRef) {
			t.Errorf("%s: a refused destination changed the style", name)
		}
	}
}

// TestHyperlinksOffIsTodaysRender: with the setting off nothing is linked,
// and the render is the one markdownBlockLines has always produced.
func TestHyperlinksOffIsTodaysRender(t *testing.T) {
	const src = "see [the docs](https://example.com/x) and ![a chart](https://example.com/c.png)\n\n" +
		"| k | v |\n|---|---|\n| a | [cell](https://example.com/cell) |"
	for _, width := range []int{20, 80} {
		plain, _ := markdownBlockLines(src, width)
		off, _ := markdownBlockLinesLinked(src, width, false)
		if strings.Join(plain, "\n") != strings.Join(off, "\n") {
			t.Errorf("width %d: hyperlinks off is not byte-identical to the default render", width)
		}
		if strings.Contains(strings.Join(off, "\n"), osc8Open) {
			t.Errorf("width %d: an OSC 8 sequence was emitted with hyperlinks off", width)
		}
		pane := strings.Join(outputLines([]apiclient.TranscriptRecord{{Type: "agent.output", Text: src}},
			levelNormal, width, lineOpts{expandKey: "v"}), "\n")
		if strings.Contains(pane, osc8Open) {
			t.Errorf("width %d: the pane emitted OSC 8 without the setting", width)
		}
	}
}

// TestHyperlinksOnLinkLabelMarkerAndReference is decision 2: the label, its
// `[n]` and the reference line's destination are the clickable text, and the
// reference block is still there.
func TestHyperlinksOnLinkLabelMarkerAndReference(t *testing.T) {
	const src = "see [the docs](https://example.com/x) now"
	on := linkedLines(src, 80)
	off, _ := markdownBlockLinesLinked(src, 80, false)
	if joinStripped(on) != joinStripped(off) {
		t.Fatalf("hyperlinks changed the text on screen:\n%s\n---\n%s", joinStripped(on), joinStripped(off))
	}
	if !strings.Contains(joinStripped(on), "[1] https://example.com/x") {
		t.Fatalf("the reference block was dropped:\n%s", joinStripped(on))
	}

	var label, ref []linkSpan
	for _, l := range on {
		spans := linkSpans(t, l)
		if strings.HasPrefix(ansi.Strip(l), "  [1] ") {
			ref = append(ref, spans...)
		} else {
			label = append(label, spans...)
		}
	}
	wantID := "vincent-" + hyperlinkDocID(src) + "-1"
	var labelText strings.Builder
	for _, s := range append(label, ref...) {
		if s.uri != "https://example.com/x" || s.id != wantID {
			t.Errorf("span %+v, want uri https://example.com/x and id %s", s, wantID)
		}
	}
	for _, s := range label {
		labelText.WriteString(s.text)
	}
	if got := labelText.String(); got != "the docs [1]" {
		t.Errorf("the linked prose is %q, want the label and its marker, and nothing around them", got)
	}
	if len(ref) != 1 || ref[0].text != "https://example.com/x" {
		t.Errorf("the reference line's link is %+v, want the destination alone", ref)
	}
}

// TestHyperlinksSurviveWrapping: a label wide enough to wrap opens and closes
// its link on every line it lands on, all under one id, and a long
// destination hard-wraps in the reference block the same way.
func TestHyperlinksSurviveWrapping(t *testing.T) {
	dest := "https://example.com/" + strings.Repeat("segment/", 12)
	src := "intro [a label long enough to wrap across several narrow lines](" + dest + ") outro"
	const width = 24
	on := linkedLines(src, width)
	off, _ := markdownBlockLinesLinked(src, width, false)
	if joinStripped(on) != joinStripped(off) {
		t.Fatalf("hyperlinks changed the layout:\n%s\n---\n%s", joinStripped(on), joinStripped(off))
	}
	ids := map[string]bool{}
	var labelLines, refText strings.Builder
	labelLineCount, refLineCount := 0, 0
	inRefs := false
	for _, l := range on {
		// The reference block follows the document's one blank line; a
		// wrapped label's own `[1]` can start a line too, so the prefix alone
		// cannot tell them apart.
		if ansi.Strip(l) == "" {
			inRefs = true
		}
		spans := linkSpans(t, l)
		if len(spans) == 0 {
			continue
		}
		for _, s := range spans {
			ids[s.id] = true
			if s.uri != dest {
				t.Errorf("a span links %q, want %q", s.uri, dest)
			}
			if inRefs {
				refText.WriteString(s.text)
			} else {
				labelLines.WriteString(s.text + "|")
			}
		}
		if inRefs {
			refLineCount++
		} else {
			labelLineCount++
		}
	}
	if len(ids) != 1 {
		t.Errorf("the pieces of one link carry %d ids, want one: %v", len(ids), ids)
	}
	if labelLineCount < 2 {
		t.Errorf("the label did not wrap onto several linked lines: %q", labelLines.String())
	}
	if refLineCount < 2 {
		t.Errorf("the destination did not hard-wrap across linked lines")
	}
	if refText.String() != dest {
		t.Errorf("the reference lines link %q, want the whole destination %q", refText.String(), dest)
	}
}

// TestHyperlinksRefusedDestinationsRenderAsToday: a destination the sanitizer
// refuses is not linked anywhere, and the render is byte-identical to the
// setting off.
func TestHyperlinksRefusedDestinationsRenderAsToday(t *testing.T) {
	for _, dest := range []string{
		"javascript:alert(1)", "JAVASCRIPT:alert(1)", "file:///etc/passwd", "data:text/html,x",
		"ftp://example.com/x", "//evil.example/x", "https:", "https://github.com@evil.example",
		"https://example.com/" + strings.Repeat("a", maxHyperlinkBytes),
		"https://gіthub.com/x", "https://example.com/café",
	} {
		for _, src := range []string{"[click](" + dest + ")", "![alt](" + dest + ")"} {
			on := strings.Join(linkedLines(src, 60), "\n")
			off := strings.Join(markdownLines(src, 60), "\n")
			if on != off {
				t.Errorf("%q renders differently with hyperlinks on", src)
			}
			if strings.Contains(on, osc8Open) {
				t.Errorf("%q was linked", src)
			}
		}
	}
	// A `;` is accepted, and reaches the escape byte for byte: only the first
	// two semicolons of an OSC 8 sequence delimit.
	const semi = "https://example.com/a;b;c"
	var uris []string
	for _, l := range linkedLines("[x]("+semi+")", 60) {
		for _, s := range linkSpans(t, l) {
			uris = append(uris, s.uri)
		}
	}
	if len(uris) == 0 {
		t.Fatal("a URL with a semicolon was not linked")
	}
	for _, u := range uris {
		if u != semi {
			t.Errorf("the escape carries %q, want %q", u, semi)
		}
	}
}

// TestHyperlinkIDCannotBeInjected: whatever the document says, the id is
// built from a digest and a number.
func TestHyperlinkIDCannotBeInjected(t *testing.T) {
	for _, doc := range []string{
		"", "plain", "\x1b]8;;\x07", "id=x;https://evil.example", "a:b;c\x9c", strings.Repeat("é", 300),
	} {
		id := hyperlinkID(hyperlinkDocID(doc), 12)
		if !linkIDShape.MatchString(id) {
			t.Errorf("id %q for document %q has bytes outside [A-Za-z0-9-]", id, doc)
		}
		src := doc + "\n\n[x](https://example.com/" + ")"
		for _, l := range linkedLines(src, 80) {
			for _, s := range linkSpans(t, l) {
				if !linkIDShape.MatchString(s.id) {
					t.Errorf("rendered id %q has bytes outside [A-Za-z0-9-]", s.id)
				}
			}
		}
	}
}

// TestHyperlinksInTablesStayBalanced: a table cell is laid out by its own
// word loop, which opens and closes the link per produced line too.
func TestHyperlinksInTablesStayBalanced(t *testing.T) {
	const src = "| key | value |\n|---|---|\n| docs | read [the manual](https://example.com/manual) first |"
	found := false
	for _, width := range []int{80, 30, 14} {
		for _, l := range linkedLines(src, width) {
			for _, s := range linkSpans(t, l) {
				found = true
				if s.uri != "https://example.com/manual" {
					t.Errorf("width %d: a cell links %q", width, s.uri)
				}
			}
		}
	}
	if !found {
		t.Fatal("a link inside a table cell was not linked")
	}
}

// TestHyperlinksNeverReachRawModeOrTheClipboard: raw shows source, and the
// clipboard payloads are built from segment text rather than styles.
func TestHyperlinksNeverReachRawModeOrTheClipboard(t *testing.T) {
	const src = "see [docs](https://example.com/x)\n\n```sh\ncurl https://example.com\n```"
	recs := []apiclient.TranscriptRecord{{Type: "agent.output", Text: src}}
	raw := strings.Join(outputLines(recs, levelNormal, 80,
		lineOpts{expandKey: "v", raw: true, hyperlinks: true}), "\n")
	if strings.Contains(raw, osc8Open) {
		t.Errorf("raw mode emitted a hyperlink:\n%q", raw)
	}
	rendered := strings.Join(outputLines(recs, levelNormal, 80,
		lineOpts{expandKey: "v", hyperlinks: true}), "\n")
	if !strings.Contains(rendered, osc8Open) {
		t.Fatalf("the rendered pane did not link with the setting on:\n%q", rendered)
	}
	for name, payload := range map[string]string{
		"plain text":  markdownPlainText(src),
		"code blocks": strings.Join(codeBlocks(src), "\n"),
	} {
		if strings.ContainsRune(payload, '\x1b') {
			t.Errorf("the %s payload carries an escape: %q", name, payload)
		}
	}
}

// TestMarkdownCacheKeysOnHyperlinks: toggling the setting re-renders instead
// of serving the memoized document.
func TestMarkdownCacheKeysOnHyperlinks(t *testing.T) {
	c := &mdCache{}
	const doc = "[x](https://example.com/x)"
	render := func(links bool) string {
		c.begin()
		defer c.sweep()
		lines, _ := c.lines(doc, 80, levelNormal, false, links)
		return strings.Join(lines, "\n")
	}
	if strings.Contains(render(false), osc8Open) {
		t.Fatal("linked with the setting off")
	}
	if !strings.Contains(render(true), osc8Open) {
		t.Fatal("toggling hyperlinks on served the memoized unlinked render")
	}
	if strings.Contains(render(false), osc8Open) {
		t.Fatal("toggling hyperlinks off served the memoized linked render")
	}
	if c.renders != 3 {
		t.Errorf("renders = %d, want 3", c.renders)
	}
}

// TestHyperlinkSettingIsOneSessionValue: the root adopts the setting from
// every config answer, both workspaces read the one holder, and a pane that is
// already built rebuilds when it changes.
func TestHyperlinkSettingIsOneSessionValue(t *testing.T) {
	links := newHyperlinkHolder()
	m := &root{views: newViews(t.Context(), links), links: links}
	chat, ok := m.views[viewChat].(*chatView)
	if !ok {
		t.Fatalf("viewChat is %T", m.views[viewChat])
	}
	task, ok := m.views[viewTask].(*taskView)
	if !ok {
		t.Fatalf("viewTask is %T", m.views[viewTask])
	}

	m.Update(boardConfigMsg{hyperlinks: true})
	if !chat.links.get() || !task.detail.links.get() {
		t.Fatal("the board's config fetch did not reach both workspaces")
	}
	m.Update(boardConfigMsg{err: errTest})
	if !links.get() {
		t.Fatal("a failed config fetch turned hyperlinks off")
	}
	m.Update(configSavedMsg{cfg: apiclient.Config{}})
	if links.get() {
		t.Fatal("the config editor's save did not reach the session")
	}
	m.Update(daemonConfigMsg{config: apiclient.Config{TUI: apiclient.ConfigTUI{Hyperlinks: true}}})
	if !links.get() {
		t.Fatal("the daemon view's config fetch did not reach the session")
	}

	// A built pane notices without being marked dirty: the setting comes
	// from the daemon, not from a key the pane handled.
	links.set(false)
	const src = "[docs](https://example.com/x)"
	d := newDetail(testCtx(t), newLevelHolder(), newRawHolder(), links)
	d.width, d.height = 80, 20
	d.selectedRun = 1
	d.applyTranscript(detailTranscriptMsg{runID: d.displayRun, records: []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: src},
	}})
	if strings.Contains(d.renderOutputPane(10), osc8Open) {
		t.Fatal("the task pane linked with the setting off")
	}
	links.set(true)
	if !strings.Contains(d.renderOutputPane(10), osc8Open) {
		t.Error("the task pane did not rebuild when hyperlinks turned on")
	}

	v := newChatView(newLevelHolder(), newRawHolder(), links)
	v.chatID = 1
	v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "done", Prompt: "ask"}}
	v.applyTranscript(chatTranscriptMsg{chatID: 1, seq: 1, records: []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: src},
	}})
	if !strings.Contains(v.bodyView(80, 10), osc8Open) {
		t.Fatal("the chat body did not link with the setting on")
	}
	links.set(false)
	if strings.Contains(v.bodyView(80, 10), osc8Open) {
		t.Error("the chat body did not rebuild when hyperlinks turned off")
	}
}
