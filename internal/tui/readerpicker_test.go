package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The reader actions (task 076). internal/tui is covered by no gate script,
// so these hermetic tests are the whole assurance — task 073's position,
// unchanged.

const readerDoc = "# Findings\n\n" +
	"The `parse` step is **wrong**.\n\n" +
	"- first\n" +
	"- second\n\n" +
	"```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```\n\n" +
	"> and a quote\n"

// stubClipboard points the write seam at a recorder for one test.
func stubClipboard(t *testing.T, err error) *[]string {
	t.Helper()
	var wrote []string
	prev := clipboardWrite
	clipboardWrite = func(text string) error {
		wrote = append(wrote, text)
		return err
	}
	t.Cleanup(func() { clipboardWrite = prev })
	return &wrote
}

// copyOne runs the payload named by label out of the first message's picker
// rows. Every caller builds its own one-document picker, so the group is the
// first one rather than a parameter.
func copyOne(t *testing.T, items []copyItem, label string) string {
	t.Helper()
	const group = "message 1"
	for _, it := range items {
		if it.group == group && it.label == label {
			return it.text
		}
	}
	t.Fatalf("no %q row under %q in %d rows", label, group, len(items))
	return ""
}

// TestRawIsTheSameSourceInBothWorkspaces is the first acceptance criterion in
// its raw form: one document, one rendering, whichever pane draws it.
func TestRawIsTheSameSourceInBothWorkspaces(t *testing.T) {
	for _, width := range []int{40, 80, 200} {
		recs := []apiclient.TranscriptRecord{{Type: "agent.output", Text: readerDoc}}
		task := outputLines(recs, levelNormal, width, lineOpts{expandKey: "v", raw: true})
		chat := outputLines(recs, levelNormal, width, lineOpts{expandKey: chatExpandKey, raw: true})
		if strings.Join(task, "\n") != strings.Join(chat, "\n") {
			t.Fatalf("width %d: the two panes drew the same raw document differently", width)
		}
		// The source is what is on screen, punctuation and all.
		body := ansi.Strip(strings.Join(task, "\n"))
		for _, want := range []string{"# Findings", "**wrong**", "```go", "- first"} {
			if !strings.Contains(body, want) {
				t.Fatalf("width %d: raw mode dropped %q:\n%s", width, want, body)
			}
		}
	}
}

// TestRawWrapsAtNarrowWidths: raw is still the pane's, not a dump.
func TestRawWrapsAtNarrowWidths(t *testing.T) {
	long := "a line of source that is far longer than the pane it has to fit inside of, several times over"
	recs := []apiclient.TranscriptRecord{{Type: "agent.output", Text: long}}
	for _, width := range []int{20, 40, 80} {
		for i, l := range outputLines(recs, levelNormal, width, lineOpts{expandKey: "v", raw: true}) {
			if got := ansi.StringWidth(l); got > width {
				t.Fatalf("width %d: raw line %d is %d cells wide: %q", width, i, got, ansi.Strip(l))
			}
		}
	}
}

// TestRawStripsTerminalControls: raw mode is an escape hatch for a surprising
// render, not a hole in §16's one chokepoint (decision 3).
func TestRawStripsTerminalControls(t *testing.T) {
	evil := "before\x1b[2Jafter\x1b]0;title\x07\x07\n\x9bmore\x00end"
	recs := []apiclient.TranscriptRecord{{Type: "agent.output", Text: evil}}
	for _, width := range []int{20, 80} {
		out := strings.Join(outputLines(recs, levelNormal, width, lineOpts{expandKey: "v", raw: true}), "\n")
		for _, r := range ansi.Strip(out) {
			if isTerminalControl(r) {
				t.Fatalf("width %d: raw output kept control %#U", width, r)
			}
		}
		if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b]0;") {
			t.Fatalf("width %d: raw output kept an escape sequence: %q", width, out)
		}
	}
}

// TestRawTouchesNothingDurable: presentation only (decision 2).
func TestRawTouchesNothingDurable(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 4
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
	d.records = []apiclient.TranscriptRecord{{Type: "agent.output", Text: readerDoc}}
	d.nextOffset = 17
	before := d.records[0]
	level := d.level.get()

	d.toggleRaw()

	if !reflect.DeepEqual(d.records[0], before) {
		t.Fatal("toggling raw mutated the record")
	}
	if d.nextOffset != 17 {
		t.Fatalf("toggling raw moved nextOffset to %d", d.nextOffset)
	}
	if d.level.get() != level {
		t.Fatalf("toggling raw changed the level to %v", d.level.get())
	}
	if !d.following {
		t.Fatal("toggling raw dropped follow")
	}
}

// TestRawIsOneSessionValue mirrors task 071's level test: one holder, both
// workspaces, and it survives leaving and reopening a view.
func TestRawIsOneSessionValue(t *testing.T) {
	level, raw := newLevelHolder(), newRawHolder()
	d := newDetail(testCtx(t), level, raw)
	v := newChatView(level, raw)

	d.toggleRaw()
	if !v.raw.get() {
		t.Fatal("raw toggled in the task workspace is not visible in the chat")
	}
	v.updateKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if d.raw.get() {
		t.Fatal("raw toggled back in the chat is not visible in the task workspace")
	}

	raw.toggle()
	if !newChatView(level, raw).raw.get() {
		t.Fatal("a freshly opened chat view lost the session's raw choice")
	}
}

// TestChatRetentionFallbackFollowsRaw: §17's answer is assistant prose too.
func TestChatRetentionFallbackFollowsRaw(t *testing.T) {
	v := chatViewFixture()
	v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "done", Prompt: "hi", ResultText: readerDoc}}

	rendered := ansi.Strip(strings.Join(v.bodyLines(80), "\n"))
	if strings.Contains(rendered, "# Findings") {
		t.Fatalf("the rendered fallback kept its Markdown punctuation:\n%s", rendered)
	}
	v.raw.toggle()
	if raw := ansi.Strip(strings.Join(v.bodyLines(80), "\n")); !strings.Contains(raw, "# Findings") {
		t.Fatalf("the retention fallback ignored raw mode:\n%s", raw)
	}
	// And it is offered to the picker.
	if len(copyDocs(v.copyDocs())) == 0 {
		t.Fatal("the retention fallback contributed no copy targets")
	}
}

// TestCopyPayloads pins what each of the three payloads is (decision 5).
func TestCopyPayloads(t *testing.T) {
	items := copyDocs([]string{readerDoc})

	md := copyOne(t, items, copyLabelMarkdown)
	if md != readerDoc {
		t.Fatalf("the markdown payload is not the stored text:\n%q", md)
	}

	plain := copyOne(t, items, copyLabelPlain)
	for _, punct := range []string{"#", "*", "`"} {
		if strings.Contains(plain, punct) {
			t.Fatalf("the plain-text payload kept %q:\n%s", punct, plain)
		}
	}
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("the plain-text payload carries ANSI:\n%q", plain)
	}
	for _, want := range []string{"Findings", "The parse step is wrong.", "• first", "func main() {"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("the plain-text payload dropped %q:\n%s", want, plain)
		}
	}

	code := copyOne(t, items, copyLabelCode)
	if want := "func main() {\n\tprintln(\"hi\")\n}"; code != want {
		t.Fatalf("the code payload is %q, want %q", code, want)
	}
}

// TestCopyPayloadsAreWidthIndependent: no pane wrapping on the clipboard
// (decision 4).
func TestCopyPayloadsAreWidthIndependent(t *testing.T) {
	// The picker is built from the source, so width cannot reach it — this
	// asserts the property by rendering the pane at both widths first, which
	// is what a real session does before pressing the key.
	recs := []apiclient.TranscriptRecord{{Type: "agent.output", Text: readerDoc}}
	var payloads [2]string
	for i, width := range []int{40, 200} {
		outputLines(recs, levelNormal, width, lineOpts{expandKey: "v"})
		var b strings.Builder
		for _, it := range copyDocs(copyDocsFromRecords(recs)) {
			b.WriteString(it.label + "\x00" + it.text + "\x00")
		}
		payloads[i] = b.String()
	}
	if payloads[0] != payloads[1] {
		t.Fatal("the clipboard payloads differ between width 40 and width 200")
	}
}

// TestCopyPayloadsAreSanitized: §16 holds at the clipboard boundary too
// (decision 4).
func TestCopyPayloadsAreSanitized(t *testing.T) {
	wrote := stubClipboard(t, nil)
	evil := "# Title\x1b[2J\n\nbody\x07 and \x9bmore\n"
	items := copyDocs([]string{evil})
	if len(items) == 0 {
		t.Fatal("no copy rows for a document that has text")
	}
	for _, it := range items {
		msg := drain(writeClipboardCmd(it.label, it.text))
		if res, ok := msg.(clipboardResultMsg); !ok || res.err != nil {
			t.Fatalf("%s: %#v", it.label, msg)
		}
	}
	for _, got := range *wrote {
		for _, r := range got {
			if isTerminalControl(r) {
				t.Fatalf("a clipboard payload kept control %#U: %q", r, got)
			}
		}
	}
}

// TestCopyPickerLists covers what the popup offers and in what order.
func TestCopyPickerLists(t *testing.T) {
	twin := "Here is the answer.\n\nno fence in this one\n"
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: twin},
		{Type: "agent.thinking", Text: "not prose"},
		{Type: "agent.output", Text: readerDoc},
		{Type: "agent.output", Text: twin},
	}
	items := copyDocs(copyDocsFromRecords(recs))

	// Newest first, and two documents with identical opening text stay
	// distinguishable by their ordinal.
	if items[0].group != "message 1" || items[0].text != twin {
		t.Fatalf("the newest document is not message 1: %#v", items[0])
	}
	groups := map[string]bool{}
	for _, it := range items {
		groups[it.group] = true
	}
	if len(groups) != 3 {
		t.Fatalf("got %d documents, want 3: %v", len(groups), groups)
	}
	// A document with no fence contributes no code row; the reasoning record
	// contributes nothing at all.
	for _, it := range items {
		if it.group != "message 2" && strings.HasPrefix(it.label, copyLabelCode) {
			t.Fatalf("%s has a code row it should not: %#v", it.group, it)
		}
		if strings.Contains(it.text, "not prose") {
			t.Fatalf("a reasoning record reached the picker: %#v", it)
		}
	}
}

// TestCopyPickerCapturesAtPickTime is the "targets remain stable" criterion:
// the rows hold text, not indices, so nothing that happens afterwards moves
// the target (decision 6).
func TestCopyPickerCapturesAtPickTime(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 4
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
	d.records = []apiclient.TranscriptRecord{{Type: "agent.output", Text: readerDoc}}
	d.width = 200

	msg, ok := drain(d.updateKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})).(openCopyPickerMsg)
	if !ok {
		t.Fatal("ctrl+y did not open the picker")
	}
	p := newReaderPicker(msg.items)

	// The world moves on: more output arrives, the front of the slice is
	// pruned, and the pane is resized.
	d.records = []apiclient.TranscriptRecord{{Type: "agent.output", Text: "something else entirely"}}
	d.width = 40
	d.outputLines()

	run, done, _ := p.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || run == nil {
		t.Fatal("enter did not pick a row")
	}
	if run.text != readerDoc {
		t.Fatalf("the pick copied %q, want the document that was on screen", run.text)
	}
}

// TestCopyPickerFilters: the search line narrows the rows, as the palette's
// does.
func TestCopyPickerFilters(t *testing.T) {
	p := newReaderPicker(copyDocs([]string{readerDoc}))
	for _, r := range "code" {
		p.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m := p.matches()
	if len(m) == 0 {
		t.Fatal("searching for \"code\" matched nothing")
	}
	for _, it := range m {
		if !strings.Contains(it.label, "code") {
			t.Fatalf("%q matched the query \"code\"", it.label)
		}
	}
	if _, done, _ := p.update(tea.KeyPressMsg{Code: tea.KeyEscape}); !done {
		t.Fatal("esc did not close the picker")
	}
}

// TestClipboardSeam covers the three outcomes a copy can have.
func TestClipboardSeam(t *testing.T) {
	t.Run("system clipboard takes it", func(t *testing.T) {
		wrote := stubClipboard(t, nil)
		msg, ok := drain(writeClipboardCmd("markdown", "hello")).(clipboardResultMsg)
		if !ok || msg.err != nil || msg.osc != "" {
			t.Fatalf("got %#v, want a plain success", msg)
		}
		if len(*wrote) != 1 || (*wrote)[0] != "hello" {
			t.Fatalf("wrote %v", *wrote)
		}
		if text, bad := msg.notice(); bad || text != "markdown copied" {
			t.Fatalf("notice %q bad=%v", text, bad)
		}
	})

	t.Run("falls over to OSC 52", func(t *testing.T) {
		stubClipboard(t, errors.New("exec: \"xclip\": not found"))
		msg, ok := drain(writeClipboardCmd("code block", "hello")).(clipboardResultMsg)
		if !ok || msg.osc != "hello" {
			t.Fatalf("got %#v, want the payload handed to the terminal", msg)
		}
		text, bad := msg.notice()
		if bad {
			t.Fatal("the fallback reads as a failure")
		}
		if !strings.Contains(text, "sent to the terminal") || !strings.Contains(text, "xclip") {
			t.Fatalf("the fallback notice %q names neither the transport nor the error", text)
		}
	})

	t.Run("nothing to copy", func(t *testing.T) {
		stubClipboard(t, nil)
		msg := drain(writeClipboardCmd("markdown", "\x1b[2J")).(clipboardResultMsg)
		text, bad := msg.notice()
		if !bad || !strings.Contains(text, "nothing to copy") {
			t.Fatalf("notice %q bad=%v", text, bad)
		}
	})
}

// TestCopyResultBecomesANotice: a key press a human made never fails
// silently, in either workspace.
func TestCopyResultBecomesANotice(t *testing.T) {
	d := newTestDetail(t)
	d.update(clipboardResultMsg{label: "markdown"})
	if d.actions.status != "markdown copied" || d.actions.statusBad {
		t.Fatalf("the task workspace said %q (bad=%v)", d.actions.status, d.actions.statusBad)
	}

	v := chatViewFixture()
	v.update(clipboardResultMsg{label: "markdown", err: errors.New("boom")})
	if !v.noteBad || !strings.Contains(v.note, "boom") {
		t.Fatalf("the chat said %q (bad=%v)", v.note, v.noteBad)
	}
}

// connectedChatRoot is a chat workspace on screen with its composer focused,
// which is where every one of these keys is hardest.
func connectedChatRoot(t *testing.T) *root {
	t.Helper()
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	v := chatViewFixture()
	v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "done", Prompt: "hi"}}
	v.turnRecords[1] = []apiclient.TranscriptRecord{{Type: "agent.output", Text: readerDoc}}
	m.views[viewChat] = v
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.switchTo(viewChat)
	if m.active != viewChat {
		t.Fatalf("fixture is on %v, want the chat workspace", m.active)
	}
	if !m.activeCapturesInput() {
		t.Fatal("fixture's composer does not hold the keyboard")
	}
	return m
}

// TestPaletteReachableFromAChat is decision 7: the palette is §15's "what can
// be done right now" surface and the chat workspace had been unable to reach
// it since task 067, because `:` types a colon into the draft.
func TestPaletteReachableFromAChat(t *testing.T) {
	m := connectedChatRoot(t)
	v := m.views[viewChat].(*chatView)

	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.palette == nil {
		t.Fatal("ctrl+p did not open the palette from a chat")
	}
	if v.composer.Value() != "" {
		t.Fatalf("ctrl+p reached the composer: %q", v.composer.Value())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.palette != nil {
		t.Fatal("esc did not close the palette")
	}

	// And the composer still owns everything it owned before: every letter,
	// both arrows, and the colon that is the palette's other key.
	for _, k := range []string{"q", "n", "v", ":"} {
		m.Update(key(k))
	}
	if m.palette != nil {
		t.Fatal("a printable key opened the palette from inside the composer")
	}
	if got := v.composer.Value(); got != "qnv:" {
		t.Fatalf("the composer holds %q, want every key typed into it", got)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if got := v.composer.Value(); got != "qnv:" {
		t.Fatalf("the arrows changed the draft to %q", got)
	}
}

// TestCopyPickerThroughTheRoot drives the whole path a human takes: the key
// in the workspace, the popup owning the keyboard, the pick, the clipboard,
// and the notice.
func TestCopyPickerThroughTheRoot(t *testing.T) {
	wrote := stubClipboard(t, nil)
	m := connectedChatRoot(t)
	v := m.views[viewChat].(*chatView)

	// The key asks for the popup with a command; the runtime delivers its
	// message, which is what a test has to do by hand.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m.Update(drain(cmd))
	if m.reader == nil {
		t.Fatal("ctrl+y did not open the copy picker")
	}
	if v.composer.Value() != "" {
		t.Fatalf("ctrl+y reached the composer: %q", v.composer.Value())
	}
	// The popup owns the keyboard: a letter searches, it does not quit.
	m.Update(key("q"))
	if m.reader == nil {
		t.Fatal("q closed the picker instead of typing into its search line")
	}
	if v.composer.Value() != "" {
		t.Fatalf("a key reached the composer under the popup: %q", v.composer.Value())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.reader != nil {
		t.Fatal("esc did not close the picker")
	}

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	m.Update(drain(cmd))
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.reader != nil {
		t.Fatal("enter left the picker open")
	}
	m.Update(drain(cmd))
	if len(*wrote) != 1 || (*wrote)[0] != readerDoc {
		t.Fatalf("the clipboard got %v, want the assistant's stored Markdown", *wrote)
	}
	if v.note != "markdown copied" || v.noteBad {
		t.Fatalf("the chat said %q (bad=%v)", v.note, v.noteBad)
	}
}

// TestReaderHintsNameTheCtrlKeys: the footer is derived from the registry, so
// what it prints is what the keys are.
func TestReaderHintsNameTheCtrlKeys(t *testing.T) {
	for _, ctx := range []bindingContext{ctxOutput, ctxChat} {
		var hints []string
		for _, b := range bindingsFor(ctx) {
			if b.key == rawToggleKey || b.key == copyPickKey {
				hints = append(hints, b.hint)
			}
		}
		if len(hints) != 2 {
			t.Fatalf("%s: the registry hints the reader actions %v", ctx, hints)
		}
		for _, h := range hints {
			if !strings.HasPrefix(h, "ctrl+") {
				t.Fatalf("%s: hint %q does not name a ctrl key", ctx, h)
			}
		}
	}
	// The chat's own footer line is hand-written rather than derived, so it
	// has to be checked against the registry by hand.
	v := chatViewFixture()
	foot := ansi.Strip(strings.Join(v.footerLines(200), "\n"))
	for _, k := range []string{rawToggleKey, copyPickKey} {
		if !strings.Contains(foot, k) {
			t.Fatalf("the chat footer does not name %s:\n%s", k, foot)
		}
	}
}

// TestPaletteRunsTheReaderActions closes the loop the palette promises: a
// listed entry replays its direct key, so an entry whose key the replay could
// not synthesize would be a row that does nothing.
func TestPaletteRunsTheReaderActions(t *testing.T) {
	for _, key := range []string{rawToggleKey, copyPickKey, chatExpandKey, "ctrl+g"} {
		if got := synthKey(key); got.String() != key {
			t.Fatalf("synthKey(%q) produces %q", key, got.String())
		}
	}

	m := connectedChatRoot(t)
	v := m.views[viewChat].(*chatView)
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.palette == nil {
		t.Fatal("ctrl+p did not open the palette")
	}
	for _, r := range "original Markdown" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := len(m.palette.matches()); got != 1 {
		t.Fatalf("the query matched %d entries, want the raw toggle alone", got)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !v.raw.get() {
		t.Fatal("running the palette entry did not toggle raw mode")
	}
	if v.composer.Value() != "" {
		t.Fatalf("the palette's replay typed into the composer: %q", v.composer.Value())
	}
}

// TestCopyPayloadsCarryTask075Blocks: the two blocks task 075 added reach the
// clipboard as the structure the pane draws (decision 5). A table becomes the
// stacked `column: value` records — the pane's own width-free form — and a
// link keeps the number the pane gave it, with the reference block that
// resolves it closing the payload, because a destination stripped of both its
// punctuation and its reference would be a destination deleted.
func TestCopyPayloadsCarryTask075Blocks(t *testing.T) {
	doc := "See [the spec](https://example.test/spec) and [it again](https://example.test/spec).\n" +
		"\n" +
		"| Step | State |\n" +
		"|---|---|\n" +
		"| build | ok |\n"
	items := copyDocs([]string{doc})

	plain := copyOne(t, items, copyLabelPlain)
	for _, punct := range []string{"](", "|", "---"} {
		if strings.Contains(plain, punct) {
			t.Fatalf("the plain-text payload kept %q:\n%s", punct, plain)
		}
	}
	for _, want := range []string{
		"See the spec [1] and it again [1].", // one number for one destination
		"▪ Step: build",
		"  State: ok",
		"[1] https://example.test/spec",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("the plain-text payload dropped %q:\n%s", want, plain)
		}
	}

	// The Markdown payload is still the stored text, table pipes and all.
	if md := copyOne(t, items, copyLabelMarkdown); md != doc {
		t.Fatalf("the markdown payload is not the stored text:\n%q", md)
	}
	// A document with no fence contributes no code row.
	for _, it := range items {
		if it.label == copyLabelCode {
			t.Fatalf("a document with no fenced block offered %q", it.label)
		}
	}
}
