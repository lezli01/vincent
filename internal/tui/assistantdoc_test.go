package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// splitDoc breaks a document into n records at line boundaries, which is
// where an adapter that split a message would have broken it. Rejoining the
// pieces with a newline reconstructs the source exactly, which is the
// property every test below leans on.
func splitDoc(text string, n int) []apiclient.TranscriptRecord {
	lines := strings.Split(text, "\n")
	out := make([]apiclient.TranscriptRecord, 0, n)
	per := max(len(lines)/n, 1)
	for i := 0; i < len(lines); i += per {
		end := min(i+per, len(lines))
		out = append(out, apiclient.TranscriptRecord{
			Type: "agent.output", Text: strings.Join(lines[i:end], "\n"),
		})
		if len(out) == n {
			if end < len(lines) {
				out[len(out)-1].Text += "\n" + strings.Join(lines[end:], "\n")
			}
			break
		}
	}
	return out
}

// splitDocAt breaks a document into exactly two records at one line boundary.
func splitDocAt(text string, line int) []apiclient.TranscriptRecord {
	lines := strings.Split(text, "\n")
	return []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: strings.Join(lines[:line], "\n")},
		{Type: "agent.output", Text: strings.Join(lines[line:], "\n")},
	}
}

// docFixtures are documents whose interesting boundaries are the ones a
// record split can land on: inside a paragraph, between a list and its nested
// items, inside a quote, between a table's header, delimiter and rows, and at
// a fence's open, interior and close.
var docFixtures = map[string]string{
	"paragraph":   "The first sentence.\nThe second one.\n\nA new paragraph.\n",
	"nested list": "Steps:\n\n- outer one\n  - inner one\n  - inner two\n- outer two\n",
	"blockquote":  "As it says:\n\n> a quoted line\n> and its continuation\n\nafter.\n",
	"table":       "Before.\n\n| Step | State |\n|---|---|\n| build | ok |\n| test | ok |\n\nAfter.\n",
	"fence":       "Run it:\n\n```sh\ngo test ./...\ngo run mage.go lint\n```\n\nThen read it.\n",
	"headings":    "# Title\n\nbody one\n\n## Sub\n\nbody two\n\n---\n\nlast\n",
}

// TestSplitDocumentRendersLikeWholeOne is #291's central property: a message
// an adapter delivered as several records renders exactly as the same source
// delivered as one. Every structurally interesting boundary is tried, across
// two records and three.
func TestSplitDocumentRendersLikeWholeOne(t *testing.T) {
	const width = 60
	for name, doc := range docFixtures {
		whole := strings.Join(plainLines(outputLines(
			[]apiclient.TranscriptRecord{{Type: "agent.output", Text: doc}},
			levelNormal, width, lineOpts{expandKey: "v"})), "\n")
		for line := 1; line < len(strings.Split(doc, "\n")); line++ {
			got := strings.Join(plainLines(outputLines(
				splitDocAt(doc, line), levelNormal, width, lineOpts{expandKey: "v"})), "\n")
			if got != whole {
				t.Fatalf("%s split at line %d rendered differently:\n%s\n--- want ---\n%s",
					name, line, got, whole)
			}
		}
		got := strings.Join(plainLines(outputLines(
			splitDoc(doc, 3), levelNormal, width, lineOpts{expandKey: "v"})), "\n")
		if got != whole {
			t.Fatalf("%s split across three records rendered differently:\n%s\n--- want ---\n%s",
				name, got, whole)
		}
	}
}

// TestRecordArrivalMovesNothingAboveTheLastBlock is decision 3's bound,
// asserted line for line rather than claimed.
//
// Joining reintroduces a growing tail at *record* granularity — a table
// header in one record and its delimiter in the next renders as a paragraph
// and then becomes a table — and the guarantee is the weaker, true one:
// nothing above the last block of the document that was already on screen
// moves when the next record arrives.
func TestRecordArrivalMovesNothingAboveTheLastBlock(t *testing.T) {
	const width = 60
	cases := []struct{ before, arriving string }{
		{"A paragraph.\n\n- one\n- two\n", "- three\n"},
		{"Before.\n\n| Step | State |", "|---|---|\n| build | ok |"},
		{"Text.\n\n```go\nx := 1", "y := 2\n```"},
		{"# Title\n\nbody", "\n\nmore body"},
	}
	for _, tc := range cases {
		before, at := markdownBlockLines(tc.before, width)
		head := 0
		for i, b := range at {
			if b == at[len(at)-1] {
				head = i
				break
			}
		}
		after, _ := markdownBlockLines(tc.before+"\n"+tc.arriving, width)
		if len(after) < head {
			t.Fatalf("%q shrank below the previous document's last block", tc.before)
		}
		for i := range head {
			if plainLines(before[i : i+1])[0] != plainLines(after[i : i+1])[0] {
				t.Fatalf("line %d moved when a record arrived:\n%q\nbecame\n%q",
					i, plainLines(before)[i], plainLines(after)[i])
			}
		}
	}
}

// TestOnlyProseAccumulates: any other record closes a document, and so does a
// run or turn boundary.
func TestOnlyProseAccumulates(t *testing.T) {
	prose := func(text string) apiclient.TranscriptRecord {
		return apiclient.TranscriptRecord{Type: "agent.output", Text: text}
	}
	closers := []apiclient.TranscriptRecord{
		{Type: "agent.thinking", Text: "thinking"},
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Bash", CallID: "t1"}}},
		{Type: "agent.tool_result", Results: []apiclient.TranscriptToolResult{{CallID: "t1"}}},
		{Type: "agent.command_output", Output: "hello"},
		{Type: "agent.raw", Line: "{}"},
		{Type: "agent.result", ResultText: "done"},
	}
	for _, closer := range closers {
		recs := []apiclient.TranscriptRecord{prose("| Step |"), closer, prose("|---|")}
		docs := assistantDocs(recs, nil)
		if len(docs) != 2 {
			t.Fatalf("%s left %d document(s), want 2", closer.Type, len(docs))
		}
		if docs[0].text != "| Step |" || docs[1].text != "|---|" {
			t.Fatalf("%s joined across the closer: %#v", closer.Type, docs)
		}
	}
	// A run or turn boundary is the edge of the window itself: each pane
	// renders one attempt's or one turn's records, so two windows are two
	// documents by construction.
	first := assistantDocs([]apiclient.TranscriptRecord{prose("| Step |")}, nil)
	second := assistantDocs([]apiclient.TranscriptRecord{prose("|---|")}, nil)
	if len(first) != 1 || len(second) != 1 || first[0].text == second[0].text {
		t.Fatal("a window boundary did not close the document")
	}
}

// TestLinkReferencesAreNumberedPerDocument: task 075 numbers destinations per
// rendered message, and #291 makes the joined document the message. One
// destination named in two records of one run gets one number and one line.
func TestLinkReferencesAreNumberedPerDocument(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "See [the spec](https://example.test/spec)."},
		{Type: "agent.output", Text: "And [it again](https://example.test/spec)."},
	}
	got := strings.Join(plainLines(outputLines(recs, levelNormal, 80, lineOpts{expandKey: "v"})), "\n")
	if n := strings.Count(got, "https://example.test/spec"); n != 1 {
		t.Fatalf("the destination is listed %d times, want once:\n%s", n, got)
	}
	if !strings.Contains(got, "[1] https://example.test/spec") {
		t.Fatalf("the reference block did not close the document:\n%s", got)
	}
	if strings.Contains(got, "[2]") {
		t.Fatalf("one destination took two numbers:\n%s", got)
	}
}

// TestSplitDocumentStaysInsideTheSubset: a split that lands mid-structure
// produces a document the parser has never seen whole. §16's guarantees hold
// on it anyway — no panic, no escape out of the pane, no control character
// and no line wider than the pane.
func TestSplitDocumentStaysInsideTheSubset(t *testing.T) {
	const width = 24
	halves := [][2]string{
		{"```go", "x := 1"},                       // an unclosed fence
		{"| a | b |", "|--"},                      // an incomplete delimiter row
		{"some **bold", "text that never closes"}, // a dangling emphasis run
		{"héllo wörld ", "and a — dash"},          // multibyte either side
		{"> quote", ">> deeper"},                  // a quote that changes depth
		{"- item", "  continuation of the item"},  // a list item's continuation
	}
	for _, h := range halves {
		recs := []apiclient.TranscriptRecord{
			{Type: "agent.output", Text: h[0]},
			{Type: "agent.output", Text: h[1]},
		}
		for _, raw := range []bool{false, true} {
			lines := outputLines(recs, levelNormal, width, lineOpts{expandKey: "v", raw: raw})
			for _, line := range plainLines(lines) {
				for _, r := range line {
					if isTerminalControl(r) {
						t.Fatalf("%q kept control %#U", line, r)
					}
				}
				if cols(line) > width {
					t.Fatalf("%q is %d cells wide, pane is %d", line, cols(line), width)
				}
			}
		}
	}
}

// splitAcrossMultibyte is the one split a byte-oriented join could corrupt: a
// rune's bytes divided between two records. Records carry text, not bytes, so
// this can only be built by hand — and the join must still produce the two
// halves as they were, not a replacement character.
func TestSplitDoesNotCorruptMultibyteText(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "日本語の"},
		{Type: "agent.output", Text: "テキスト"},
	}
	docs := assistantDocs(recs, nil)
	if len(docs) != 1 || docs[0].text != "日本語の\nテキスト" {
		t.Fatalf("the join corrupted multibyte text: %#v", docs)
	}
}

// TestPausedAnchorSurvivesRebuilds is the anchor's whole point: the four
// things that actually move a paused reader — a resize, a maxRecords prune, a
// level cycle and a raw toggle — leave the topmost visible block where it was.
func TestPausedAnchorSurvivesRebuilds(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 4
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
	recs := make([]apiclient.TranscriptRecord, 0, 40)
	for i := range 20 {
		recs = append(recs,
			apiclient.TranscriptRecord{Type: "agent.output", Text: "paragraph " + string(rune('a'+i))},
			apiclient.TranscriptRecord{Type: "agent.thinking", Text: "thought"},
		)
	}
	d.applyTranscript(detailTranscriptMsg{runID: d.displayRun, records: recs})
	d.width, d.height = 80, 20
	d.renderOutputPane(10)

	// Scroll away from the tail and note what is at the top.
	d.following = false
	d.vp.SetYOffset(12)
	topOf := func() lineAnchor { return anchorAt(d.anchors, d.vp.YOffset()) }
	want := topOf()
	if want.rec == 0 {
		t.Fatal("the paused reader is looking at nothing anchorable")
	}

	for _, step := range []struct {
		name string
		do   func()
	}{
		{"resize", func() { d.width = 50 }},
		{"level cycle", func() { d.cycleLevel(); d.outputDirty = true }},
		{"raw toggle", func() { d.toggleRaw(); d.outputDirty = true }},
		{"prune", func() {
			d.appendChunk(apiclient.OutputNote{
				Type: "agent.output", RunID: d.displayRun, Offset: 99,
				Payload: []byte(`{"text":"a later message"}`),
			})
			d.outputDirty = true
		}},
	} {
		step.do()
		d.renderOutputPane(10)
		if got := topOf(); got.rec != want.rec {
			t.Fatalf("a %s moved the paused reader off document %d, onto %d",
				step.name, want.rec, got.rec)
		}
	}

	// Following is untouched: it keeps the bottom anchor across the same
	// rebuilds.
	d.following = true
	d.width = 70
	d.renderOutputPane(10)
	if !d.vp.AtBottom() {
		t.Fatal("a resize left a following pane off the bottom")
	}
}

// TestChatPausedAnchorSurvivesRebuilds is the same property in the other
// workspace, which shares the renderer and now shares the anchor.
func TestChatPausedAnchorSurvivesRebuilds(t *testing.T) {
	v := chatViewFixture()
	for seq := 1; seq <= 6; seq++ {
		v.turns = append(v.turns, apiclient.ChatTurn{
			ID: int64(seq), Seq: seq, State: "done", Prompt: "ask " + string(rune('a'+seq)),
		})
		v.applyTranscript(chatTranscriptMsg{chatID: v.chatID, seq: seq, records: []apiclient.TranscriptRecord{
			{Type: "agent.output", Text: "# Answer\n\nbody of turn " + string(rune('a'+seq))},
			{Type: "agent.output", Text: "\nand its second half"},
		}})
	}
	v.bodyView(80, 10)
	v.following = false
	v.vp.SetYOffset(8)
	want := anchorAt(v.anchors, v.vp.YOffset())
	if want.rec == 0 {
		t.Fatal("the paused reader is looking at nothing anchorable")
	}

	for _, step := range []struct {
		name  string
		do    func()
		width int
	}{
		{"resize", func() {}, 50},
		{"level cycle", func() { v.level.cycle(); v.bodyDirty = true }, 50},
		{"raw toggle", func() { v.raw.toggle(); v.bodyDirty = true }, 50},
	} {
		step.do()
		v.bodyView(step.width, 10)
		if got := anchorAt(v.anchors, v.vp.YOffset()); got.rec != want.rec {
			t.Fatalf("a %s moved the paused reader off document %d, onto %d",
				step.name, want.rec, got.rec)
		}
	}

	v.following = true
	v.bodyDirty = true
	v.bodyView(70, 10)
	if !v.vp.AtBottom() {
		t.Fatal("a rebuild left a following conversation off the bottom")
	}
}

// TestMarkdownCacheKeysOnEverythingThatChangesTheResult: identical source at
// one width, level and raw setting renders once; each of the three
// invalidates. Renders are counted, not timed — a timing assertion would make
// the suite a benchmark.
func TestMarkdownCacheKeysOnEverythingThatChangesTheResult(t *testing.T) {
	c := &mdCache{}
	const doc = "# Title\n\nbody\n"
	render := func(width int, level outputLevel, raw bool) {
		c.begin()
		c.lines(doc, width, level, raw)
		c.sweep()
	}
	render(80, levelNormal, false)
	render(80, levelNormal, false)
	if c.renders != 1 {
		t.Fatalf("identical source rendered %d times, want 1", c.renders)
	}
	render(40, levelNormal, false)
	render(40, levelVerbose, false)
	render(40, levelVerbose, true)
	if c.renders != 4 {
		t.Fatalf("width, level and raw did not each invalidate: %d renders, want 4", c.renders)
	}
	// A pass that does not touch an entry drops it, which is what bounds the
	// memo without a size limit.
	c.begin()
	c.sweep()
	if len(c.entries) != 0 {
		t.Fatalf("the sweep kept %d untouched entries", len(c.entries))
	}
}

// TestChunkRerendersOneDocument: a chunk extending one document re-renders
// that document and no other. That is the whole point of keying on the
// document — the pane was re-rendering every record on every chunk, at the
// daemon's coalescing rate.
func TestChunkRerendersOneDocument(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 4
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
	d.applyTranscript(detailTranscriptMsg{runID: d.displayRun, records: []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "first document"},
		{Type: "agent.thinking", Text: "thought"},
		{Type: "agent.output", Text: "second document"},
	}})
	d.width = 80
	d.outputLines()
	before := d.mdcache.renders
	if before != 2 {
		t.Fatalf("the first pass rendered %d documents, want 2", before)
	}
	d.outputLines()
	if d.mdcache.renders != before {
		t.Fatalf("an unchanged pane re-rendered %d document(s)", d.mdcache.renders-before)
	}
	d.appendChunk(apiclient.OutputNote{
		Type: "agent.output", RunID: d.displayRun, Offset: 9,
		Payload: []byte(`{"text":"and its second half"}`),
	})
	d.outputLines()
	if got := d.mdcache.renders - before; got != 1 {
		t.Fatalf("a chunk re-rendered %d documents, want 1", got)
	}
}
