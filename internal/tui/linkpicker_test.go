package tui

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The link picker (task 110). internal/tui is covered by no gate script, so
// these hermetic tests are the whole assurance — task 073's position.

// linkDoc links from every block kind the renderer numbers in, repeats one
// destination, carries an image and a label with inline Markdown, and ends on
// destinations enter must refuse.
const linkDoc = "# See [the spec](https://example.test/spec)\n\n" +
	"Read [**the** `guide`](https://example.test/guide) and [the spec again](https://example.test/spec).\n\n" +
	"- ![a diagram](https://example.test/d.png)\n" +
	"- ![](https://example.test/bare.png)\n\n" +
	"> quoted [mail me](mailto:ops@example.test)\n\n" +
	"| what | where |\n|---|---|\n| file | [passwd](file:///etc/passwd) |\n\n" +
	"[run](javascript:alert(1)) [local](docs/readme.md) [broken](http://%zz)\n"

// TestMarkdownLinksMatchTheReferenceBlock is decision 6: the picker's numbers
// are the pane's numbers. The expectation is read out of markdownBlockLines'
// own reference block rather than written down twice, so the two cannot
// drift.
func TestMarkdownLinksMatchTheReferenceBlock(t *testing.T) {
	links := markdownLinks(linkDoc)

	lines, _ := markdownBlockLines(linkDoc, 400)
	ref := regexp.MustCompile(`^\s*\[(\d+)\] (\S+)$`)
	var fromPane []mdLink
	for _, l := range lines {
		if m := ref.FindStringSubmatch(ansi.Strip(l)); m != nil {
			n, _ := strconv.Atoi(m[1])
			fromPane = append(fromPane, mdLink{n: n, dest: m[2]})
		}
	}
	if len(fromPane) != len(links) {
		t.Fatalf("the pane numbers %d destinations, markdownLinks %d:\n%v\n%v", len(fromPane), len(links), fromPane, links)
	}
	for i, l := range links {
		if l.n != fromPane[i].n || l.dest != fromPane[i].dest {
			t.Fatalf("link %d is [%d] %s, the pane's is [%d] %s", i, l.n, l.dest, fromPane[i].n, fromPane[i].dest)
		}
	}

	want := []mdLink{
		{n: 1, label: "the spec", dest: "https://example.test/spec"}, // first label wins
		{n: 2, label: "the guide", dest: "https://example.test/guide"},
		{n: 3, label: "a diagram", dest: "https://example.test/d.png", image: true},
		{n: 4, label: "https://example.test/bare.png", dest: "https://example.test/bare.png", image: true},
		{n: 5, label: "mail me", dest: "mailto:ops@example.test"},
		{n: 6, label: "passwd", dest: "file:///etc/passwd"},
		{n: 7, label: "run", dest: "javascript:alert(1)"},
		{n: 8, label: "local", dest: "docs/readme.md"},
		{n: 9, label: "broken", dest: "http://%zz"},
	}
	if !reflect.DeepEqual(links, want) {
		t.Fatalf("markdownLinks:\n got %#v\nwant %#v", links, want)
	}
}

// TestMarkdownLinksNumberAJoinedDocument: a destination named in two records
// of one document gets one number (task 077 decision 2), so it gets one row.
func TestMarkdownLinksNumberAJoinedDocument(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "first [here](https://example.test/a)"},
		{Type: "agent.output", Text: "then [there](https://example.test/b) and [here again](https://example.test/a)"},
	}
	docs := copyDocsFromRecords(recs, nil)
	if len(docs) != 1 {
		t.Fatalf("got %d documents, want the two records joined into one", len(docs))
	}
	items := linkDocs(docs)
	got := make([]string, 0, len(items))
	for _, it := range items {
		got = append(got, strconv.Itoa(it.link.n)+" "+it.link.label+" "+it.link.dest)
	}
	want := []string{"1 here https://example.test/a", "2 there https://example.test/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows %q, want %q", got, want)
	}
}

// TestLinkPickerRows covers what the popup offers and in what order.
func TestLinkPickerRows(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "old [one](https://example.test/old)"},
		{Type: "agent.thinking", Text: "not prose [hidden](https://example.test/thinking)"},
		{Type: "agent.output", Text: "no links in this one"},
		{Type: "tool.call", Text: "[tool](https://example.test/tool)"},
		{Type: "agent.output", Text: "new [two](https://example.test/new) and [three](mailto:x@example.test)"},
	}
	items := linkDocs(copyDocsFromRecords(recs, nil))

	var got []string
	for _, it := range items {
		got = append(got, it.group+" ["+strconv.Itoa(it.link.n)+"] "+it.link.dest)
	}
	// Newest first; the linkless document takes an ordinal and no group, so
	// "message 3" names the same document the copy picker calls that; the
	// reasoning and tool records contribute nothing.
	want := []string{
		"message 1 [1] https://example.test/new",
		"message 1 [2] mailto:x@example.test",
		"message 3 [1] https://example.test/old",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n got %q\nwant %q", got, want)
	}
	copyGroups := map[string]string{}
	for _, it := range copyDocs(copyDocsFromRecords(recs, nil)) {
		copyGroups[it.group] = it.ref.text
	}
	for _, it := range items {
		if copyGroups[it.group] != it.ref.text {
			t.Fatalf("%s is a different document in the copy picker", it.group)
		}
	}

	p := newLinkPicker(items)
	for _, tc := range []struct{ query, want string }{
		{"two", "https://example.test/new"},              // label
		{"example.test/old", "https://example.test/old"}, // destination
		{"message 3", "https://example.test/old"},        // group
		{"MAILTO", "mailto:x@example.test"},              // case-folded
	} {
		p.input.SetValue(tc.query)
		m := p.matches()
		if len(m) != 1 || m[0].link.dest != tc.want {
			t.Fatalf("searching %q matched %v, want %s alone", tc.query, m, tc.want)
		}
	}
}

// TestLinkPickerMarksRefusedRows is decision 3: a destination openURLCmd
// would refuse is still listed, marked, and copyable — and the mark is
// openableURL's answer, not a second rule.
func TestLinkPickerMarksRefusedRows(t *testing.T) {
	items := linkDocs(literalDocs(linkDoc))
	refused := map[string]bool{}
	for _, it := range items {
		if (it.refused == nil) != (openableURL(it.link.dest) == nil) {
			t.Fatalf("%s: the row's mark disagrees with openableURL", it.link.dest)
		}
		if it.refused != nil {
			refused[it.link.dest] = true
		}
	}
	for _, dest := range []string{"mailto:ops@example.test", "file:///etc/passwd", "javascript:alert(1)", "docs/readme.md", "http://%zz"} {
		if !refused[dest] {
			t.Fatalf("%s is not listed as copy only (refused: %v)", dest, refused)
		}
	}
	for _, dest := range []string{"https://example.test/spec", "https://example.test/d.png"} {
		if refused[dest] {
			t.Fatalf("%s is marked copy only", dest)
		}
	}

	out := ansi.Strip(newLinkPicker(items).render(200, 40))
	if strings.Count(out, linkCopyOnly) != len(refused) {
		t.Fatalf("the popup marks %d rows copy only, want %d:\n%s", strings.Count(out, linkCopyOnly), len(refused), out)
	}
}

// TestLinkPickerThroughTheChatRoot drives the path a human takes in a chat,
// with the composer holding the keyboard: the key, the popup, open and copy,
// the effect, and the notice in the chat's own note.
func TestLinkPickerThroughTheChatRoot(t *testing.T) {
	opened := withFakeOpener(t, nil)
	wrote := stubClipboard(t, nil)
	m := connectedChatRoot(t)
	v := m.views[viewChat].(*chatView)
	v.turnRecords[1] = []apiclient.TranscriptRecord{{Type: "agent.output", Text: linkDoc}}
	v.bodyDirty = true

	openPicker := func() {
		t.Helper()
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
		m.Update(drain(cmd))
		if m.linkPick == nil {
			t.Fatal("ctrl+l did not open the link picker")
		}
	}
	openPicker()
	if v.composer.Value() != "" {
		t.Fatalf("ctrl+l reached the composer: %q", v.composer.Value())
	}
	m.Update(key("q"))
	if m.linkPick == nil || v.composer.Value() != "" {
		t.Fatalf("q escaped the popup (open=%v, composer %q)", m.linkPick != nil, v.composer.Value())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.linkPick != nil {
		t.Fatal("esc did not close the link picker")
	}

	// enter on an http(s) row opens exactly its destination.
	openPicker()
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.linkPick != nil {
		t.Fatal("enter left the link picker open")
	}
	m.Update(drain(cmd))
	if len(*opened) != 1 || (*opened)[0] != "https://example.test/guide" {
		t.Fatalf("the opener got %v, want the guide", *opened)
	}
	if v.note != "opened https://example.test/guide" || v.noteBad {
		t.Fatalf("the chat said %q (bad=%v)", v.note, v.noteBad)
	}

	// enter on a refused row opens nothing and says why.
	openPicker()
	for _, r := range "mail me" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(drain(cmd))
	if len(*opened) != 1 {
		t.Fatalf("a refused destination reached the opener: %v", *opened)
	}
	if !v.noteBad || !strings.Contains(v.note, `"mailto"`) {
		t.Fatalf("the chat said %q (bad=%v), want the refusal naming the scheme", v.note, v.noteBad)
	}

	// ctrl+y copies it all the same.
	openPicker()
	for _, r := range "mail me" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if m.linkPick != nil || m.reader != nil {
		t.Fatal("ctrl+y in the link picker left a popup open")
	}
	m.Update(drain(cmd))
	if len(*wrote) != 1 || (*wrote)[0] != "mailto:ops@example.test" {
		t.Fatalf("the clipboard got %v, want the mailto destination", *wrote)
	}
	if v.note != "link [5] copied" || v.noteBad {
		t.Fatalf("the chat said %q (bad=%v)", v.note, v.noteBad)
	}
}

// yielded runs cmd the way the runtime would — batches flattened, each command
// on its own goroutine so a ticker batched beside it cannot hold the test —
// and returns the first message of type T it produces. The task workspace
// batches its detail's commands with its own, so a key's message arrives
// inside a tea.BatchMsg there.
func yielded[T tea.Msg](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	msgs := make(chan tea.Msg, 16)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, b := range batch {
					run(b)
				}
				return
			}
			msgs <- msg
		}()
	}
	run(cmd)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-msgs:
			if want, ok := msg.(T); ok {
				return want
			}
		case <-deadline:
			var zero T
			t.Fatalf("no %T was produced", zero)
			return zero
		}
	}
}

// taskOutputRoot is the task workspace on its Output tab, showing linkDoc.
func taskOutputRoot(t *testing.T) (*root, *taskView) {
	t.Helper()
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	d := detailOf(m)
	d.taskID = 4
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
	d.records = []apiclient.TranscriptRecord{{Type: "agent.output", Text: linkDoc}}
	m.switchTo(viewTask)
	tv := m.views[viewTask].(*taskView)
	tv.setTab(taskTabOutput)
	return m, tv
}

// TestLinkPickerThroughTheTaskRoot is the same path in the task workspace,
// and the half decision 5 is about: the notices land in the pane's status,
// never in the pull-request note.
func TestLinkPickerThroughTheTaskRoot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		openErr error
		want    string
		bad     bool
	}{
		{"opens", nil, "opened https://example.test/spec", false},
		{"opener fails", errors.New("no browser opener found"), "could not open https://example.test/spec: no browser opener found", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened := withFakeOpener(t, tc.openErr)
			m, tv := taskOutputRoot(t)
			d := detailOf(m)

			_, cmd := m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
			m.Update(yielded[openLinkPickerMsg](t, cmd))
			if m.linkPick == nil {
				t.Fatal("ctrl+l did not open the link picker from the Output tab")
			}
			_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m.Update(drain(cmd))
			if len(*opened) != 1 || (*opened)[0] != "https://example.test/spec" {
				t.Fatalf("the opener got %v", *opened)
			}
			if d.actions.status != tc.want || d.actions.statusBad != tc.bad {
				t.Fatalf("the pane said %q (bad=%v), want %q", d.actions.status, d.actions.statusBad, tc.want)
			}
			if tv.pullNote != "" || tv.pullNoteBad {
				t.Fatalf("the pull-request note spoke for a link: %q", tv.pullNote)
			}
		})
	}

	t.Run("copies", func(t *testing.T) {
		wrote := stubClipboard(t, nil)
		m, _ := taskOutputRoot(t)
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
		m.Update(yielded[openLinkPickerMsg](t, cmd))
		_, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
		m.Update(drain(cmd))
		if len(*wrote) != 1 || (*wrote)[0] != "https://example.test/spec" {
			t.Fatalf("the clipboard got %v", *wrote)
		}
		if got := detailOf(m).actions.status; got != "link [1] copied" {
			t.Fatalf("the pane said %q", got)
		}
	})
}

// TestLinkPickerIsSanitized: control bytes inside a destination reach neither
// the clipboard, the popup nor the opener's argument.
func TestLinkPickerIsSanitized(t *testing.T) {
	opened := withFakeOpener(t, nil)
	wrote := stubClipboard(t, nil)
	evil := "see [x](https://evil.test/a\x1b]52;c;aGk=\x07b\x01c\u009bd)"
	items := linkDocs(literalDocs(evil))
	if len(items) != 1 {
		t.Fatalf("got %d rows for one link", len(items))
	}
	it := items[0]
	if msg := drain(openLinkCmd(pickLink(it, nil))).(linkOpenedMsg); msg.err != nil {
		t.Fatalf("the sanitized destination was refused: %v", msg.err)
	}
	drain(writeClipboardCmd(linkLabel(it), pickLink(it, nil)))

	payloads := append(append([]string{}, *opened...), *wrote...)
	if len(payloads) != 2 {
		t.Fatalf("opened %v, wrote %v", *opened, *wrote)
	}
	for _, got := range payloads {
		for _, r := range got {
			if isTerminalControl(r) {
				t.Fatalf("a payload kept control %#U: %q", r, got)
			}
		}
	}
	out := newLinkPicker(items).render(60, 18)
	for _, bad := range []string{"\x07", "\x01", "\u009b", "\x1b]", "52;c;"} {
		if strings.Contains(out, bad) {
			t.Fatalf("the popup drew %q:\n%q", bad, out)
		}
	}
}

// TestLinkPickerResolves is decision 2's reference rule: a pick delivers the
// destination the row showed after more records arrive, after a resize, and
// after a prune — and falls back to the captured one when the document is gone
// or its `[n]` changed.
func TestLinkPickerResolves(t *testing.T) {
	newDetail := func(t *testing.T, recs []apiclient.TranscriptRecord) *detail {
		t.Helper()
		d := newTestDetail(t)
		d.taskID = 4
		loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
		d.applyTranscript(detailTranscriptMsg{runID: d.displayRun, records: recs})
		return d
	}
	pick := func(t *testing.T, d *detail, n int, change func()) string {
		t.Helper()
		msg, ok := drain(d.updateKey(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})).(openLinkPickerMsg)
		if !ok {
			t.Fatal("ctrl+l did not open the link picker")
		}
		change()
		for _, it := range msg.items {
			if it.link.n == n && it.group == "message 1" {
				return pickLink(it, msg.resolve)
			}
		}
		t.Fatalf("no [%d] row in %v", n, msg.items)
		return ""
	}
	two := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "[a](https://example.test/a)"},
		{Type: "agent.output", Text: "[b](https://example.test/b)"},
	}

	t.Run("records arrive", func(t *testing.T) {
		d := newDetail(t, two)
		got := pick(t, d, 2, func() {
			d.appendChunk(apiclient.OutputNote{
				Type: "agent.output", RunID: d.displayRun, Offset: 99,
				Payload: json.RawMessage(`{"type":"agent.output","text":"[c](https://example.test/c)"}`),
			})
		})
		if got != "https://example.test/b" {
			t.Fatalf("picked %q", got)
		}
	})

	t.Run("resize and raw", func(t *testing.T) {
		d := newDetail(t, two)
		got := pick(t, d, 1, func() {
			d.width = 30
			d.toggleRaw()
			d.outputLines()
		})
		if got != "https://example.test/a" {
			t.Fatalf("picked %q", got)
		}
	})

	t.Run("prune takes the document's first record", func(t *testing.T) {
		recs := append([]apiclient.TranscriptRecord{}, two...)
		for len(recs) < maxRecords {
			recs = append(recs, apiclient.TranscriptRecord{Type: "agent.thinking", Text: "thought"})
		}
		recs[len(recs)-1] = apiclient.TranscriptRecord{Type: "agent.output", Text: "the newest message"}
		d := newDetail(t, recs)
		msg, ok := drain(d.updateKey(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})).(openLinkPickerMsg)
		if !ok || len(msg.items) != 2 {
			t.Fatalf("got %#v, want the two links of message 2", msg)
		}
		d.appendChunk(apiclient.OutputNote{
			Type: "agent.thinking", RunID: d.displayRun, Offset: 99,
			Payload: json.RawMessage(`{"type":"agent.thinking","text":"one more"}`),
		})
		if len(d.records) != maxRecords || d.records[0].Text != two[1].Text {
			t.Fatal("the fixture did not prune exactly the document's first record")
		}
		// [2] is now [1] of a document with a new identity: the row still
		// delivers what it showed.
		if got := pickLink(msg.items[1], msg.resolve); got != "https://example.test/b" {
			t.Fatalf("picked %q after the prune", got)
		}
	})

	t.Run("document gone", func(t *testing.T) {
		d := newDetail(t, two)
		got := pick(t, d, 1, func() {
			d.applyTranscript(detailTranscriptMsg{
				runID:   d.displayRun,
				records: []apiclient.TranscriptRecord{{Type: "agent.output", Text: "[z](https://example.test/z)"}},
			})
		})
		if got != "https://example.test/a" {
			t.Fatalf("picked %q", got)
		}
	})

	t.Run("number changed", func(t *testing.T) {
		it := linkDocs([]copyDoc{{seq: 7, text: "[a](https://example.test/a)", ok: true}})[0]
		resolve := func(seq int64) (string, bool) {
			if seq != 7 {
				t.Fatalf("resolved seq %d, want the row's document", seq)
			}
			return "[other](https://example.test/other)", true
		}
		if got := pickLink(it, resolve); got != "https://example.test/a" {
			t.Fatalf("picked %q, want the captured destination", got)
		}
	})
}

// TestLinkPickerRawChangesNothing: links are derived from the source, not from
// what is drawn, so raw mode offers the same rows.
func TestLinkPickerRawChangesNothing(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 4
	loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
	d.records = []apiclient.TranscriptRecord{{Type: "agent.output", Text: linkDoc}}

	rows := func() []linkItem {
		msg, ok := drain(d.updateKey(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})).(openLinkPickerMsg)
		if !ok {
			t.Fatal("ctrl+l did not open the link picker")
		}
		return msg.items
	}
	rendered := rows()
	d.toggleRaw()
	if !d.raw.get() {
		t.Fatal("the fixture is not in raw mode")
	}
	if raw := rows(); !reflect.DeepEqual(rendered, raw) {
		t.Fatalf("raw mode changed the rows:\n%v\n%v", rendered, raw)
	}
}

// TestLinkPickerShowsTheWholeDestination is decision 4: the row truncates,
// the popup does not — the cursor row's destination is drawn whole, wrapped
// inside the frame at any width.
func TestLinkPickerShowsTheWholeDestination(t *testing.T) {
	long := "https://example.test/" + strings.Repeat("segment/", 12) + "end?q=1"
	items := linkDocs(literalDocs("[long](" + long + ") and [short](https://example.test/s)"))
	for _, w := range []int{24, 40, 64} {
		out := newLinkPicker(items).render(w, 18)
		lines := strings.Split(out, "\n")
		var joined strings.Builder
		for _, l := range lines {
			if got := ansi.StringWidth(l); got > w {
				t.Fatalf("width %d: a line is %d wide: %q", w, got, ansi.Strip(l))
			}
			plain := ansi.Strip(l)
			plain = strings.TrimSuffix(strings.TrimPrefix(plain, "│"), "│")
			joined.WriteString(strings.TrimSpace(plain))
		}
		if !strings.Contains(joined.String(), long) {
			t.Fatalf("width %d: the whole destination is not on screen:\n%s", w, ansi.Strip(out))
		}
		if !strings.Contains(ansi.Strip(out), "enter open") {
			t.Fatalf("width %d: the key hint is gone:\n%s", w, ansi.Strip(out))
		}
	}
}

// TestPaletteRunsTheLinkPicker is TestPaletteRunsTheReaderActions for the
// third key, in both contexts: the palette's replay synthesizes ctrl+l, and
// the picker it asks for opens.
func TestPaletteRunsTheLinkPicker(t *testing.T) {
	if got := synthKey(linkPickKey); got.String() != linkPickKey {
		t.Fatalf("synthKey(%q) produces %q", linkPickKey, got.String())
	}
	chat := connectedChatRoot(t)
	chat.views[viewChat].(*chatView).turnRecords[1] = []apiclient.TranscriptRecord{{Type: "agent.output", Text: linkDoc}}
	task, _ := taskOutputRoot(t)

	for name, m := range map[string]*root{"chat": chat, "task": task} {
		t.Run(name, func(t *testing.T) {
			m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
			if m.palette == nil {
				t.Fatal("ctrl+p did not open the palette")
			}
			for _, r := range "list the links" {
				m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			if got := len(m.palette.matches()); got != 1 {
				t.Fatalf("the query matched %d entries, want the link picker alone", got)
			}
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m.Update(yielded[openLinkPickerMsg](t, cmd))
			if m.linkPick == nil || len(m.linkPick.items) == 0 {
				t.Fatal("running the palette entry did not open the link picker")
			}
		})
	}
}
