package tui

// The chat search screen (task 158, #1022, §15): a query box over
// GET /v1/chats/search and its results grouped by chat. enter on a match
// opens the chat on that turn; esc from the chat comes back here with the
// query and the cursor where they were.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
)

// chatSearchDebounce is how long typing must pause before a search is sent:
// long enough that a word typed at speed is one request, short enough that
// the results feel live.
const chatSearchDebounce = 250 * time.Millisecond

type (
	// chatSearchDueMsg fires a debounced search; gen says which keystroke
	// armed it, so only the last one in a burst asks.
	chatSearchDueMsg struct{ gen int }
	// chatSearchResultMsg is a search's answer, for the query gen asked.
	chatSearchResultMsg struct {
		gen     int
		query   string
		results []apiclient.ChatSearchResult
		err     error
	}
)

// chatSearchRow is one cursor stop: a chat's header (match -1) or one of
// its matches.
type chatSearchRow struct {
	chat  int
	match int
}

type chatSearchView struct {
	client  *apiclient.Client
	query   textField
	gen     int
	asked   string // the query the shown results answer
	results []apiclient.ChatSearchResult
	rows    []chatSearchRow
	cursor  int
	top     int
	errText string
	loading bool
}

func newChatSearchView() *chatSearchView {
	q := newTextField()
	q.SetPlaceholder("search every chat's title, prompts and answers — live and archived")
	q.SetPrompt(ic("⌕ "))
	return &chatSearchView{query: q}
}

func (v *chatSearchView) title() string { return "search chats" }

// setClient takes the client, and after a reconnect re-asks the query on
// screen: the results it shows may predate chats the daemon has since seen.
func (v *chatSearchView) setClient(c *apiclient.Client) tea.Cmd {
	v.client = c
	if strings.TrimSpace(v.query.Value()) == "" {
		return nil
	}
	v.gen++
	return v.searchCmd()
}

// capturesInput: the query box always has the keyboard, so `q` is typed
// rather than quitting.
func (v *chatSearchView) capturesInput() bool { return true }

func (v *chatSearchView) paste(text string) tea.Cmd {
	var cmd tea.Cmd
	v.query, cmd = v.query.Update(tea.PasteMsg{Content: text})
	return tea.Batch(cmd, v.arm())
}

func (v *chatSearchView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case viewActivatedMsg:
		return v, v.query.Focus()
	case chatSearchDueMsg:
		if msg.gen != v.gen {
			return v, nil
		}
		return v, v.searchCmd()
	case chatSearchResultMsg:
		if msg.gen != v.gen {
			return v, nil
		}
		v.loading = false
		if msg.err != nil {
			v.errText = chatSearchError(msg.err)
			return v, nil
		}
		v.errText = ""
		v.asked = msg.query
		v.results = msg.results
		v.buildRows()
		return v, nil
	case tea.KeyPressMsg:
		return v.updateKey(msg)
	}
	return v, nil
}

func (v *chatSearchView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return v, backToTab
	case "up":
		v.move(-1)
		return v, nil
	case "down":
		v.move(1)
		return v, nil
	case "pgup":
		v.move(-10)
		return v, nil
	case "pgdown":
		v.move(10)
		return v, nil
	case "enter":
		return v, v.openCursor()
	}
	before := v.query.Value()
	var cmd tea.Cmd
	v.query, cmd = v.query.Update(msg)
	if v.query.Value() == before {
		return v, cmd
	}
	return v, tea.Batch(cmd, v.arm())
}

// arm starts the debounce for the query as it now stands.
func (v *chatSearchView) arm() tea.Cmd {
	v.gen++
	gen := v.gen
	if strings.TrimSpace(v.query.Value()) == "" {
		v.results, v.rows, v.asked, v.errText, v.loading = nil, nil, "", "", false
		v.cursor, v.top = 0, 0
		return nil
	}
	return tea.Tick(chatSearchDebounce, func(time.Time) tea.Msg { return chatSearchDueMsg{gen: gen} })
}

func (v *chatSearchView) searchCmd() tea.Cmd {
	client, gen, q := v.client, v.gen, strings.TrimSpace(v.query.Value())
	if client == nil || q == "" {
		return nil
	}
	v.loading = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		res, err := client.SearchChats(ctx, q, apiclient.SearchChatsOptions{})
		return chatSearchResultMsg{gen: gen, query: q, results: res, err: err}
	}
}

func chatSearchError(err error) string {
	var apiErr *apiclient.Error
	if errors.As(err, &apiErr) && apiErr.Code == apiclient.CodeQueryTooShort {
		return "every word needs at least three characters"
	}
	return errString(err)
}

func (v *chatSearchView) buildRows() {
	v.rows = v.rows[:0]
	for ci := range v.results {
		v.rows = append(v.rows, chatSearchRow{chat: ci, match: -1})
		for mi := range v.results[ci].Matches {
			v.rows = append(v.rows, chatSearchRow{chat: ci, match: mi})
		}
	}
	v.cursor, v.top = 0, 0
	// Land on the first match rather than its header: a match is what enter
	// is most often for.
	if len(v.rows) > 1 {
		v.cursor = 1
	}
}

func (v *chatSearchView) move(d int) {
	if len(v.rows) == 0 {
		return
	}
	v.cursor = max(0, min(len(v.rows)-1, v.cursor+d))
}

// openCursor opens the chat under the cursor: on a turn's match, at that
// turn; on its header or a title match, where a chat normally opens. The
// back target is this screen, so esc returns to the query.
func (v *chatSearchView) openCursor() tea.Cmd {
	if v.cursor < 0 || v.cursor >= len(v.rows) {
		return nil
	}
	r := v.rows[v.cursor]
	res := v.results[r.chat]
	seq := 0
	if r.match >= 0 {
		seq = res.Matches[r.match].Seq
	}
	id, pid := res.Chat.ID, res.Chat.ProjectID
	return func() tea.Msg { return openChatMsg{id: id, projectID: pid, back: viewChatSearch, seq: seq} }
}

func (v *chatSearchView) render(width, height int) string {
	if width < 4 || height < 4 {
		return ""
	}
	v.query.SetWidth(max(width-6, 10))
	box := drawBox(toneBlue.Bold(true).Render(ic("⌕ Search all chats")), v.summary(),
		fieldRows("", v.query), width, 3, edgesRounded, styleDim)
	lines := append([]string{}, box...)
	bodyH := max(height-len(lines), 0)
	lines = append(lines, v.bodyLines(width, bodyH)...)
	return strings.Join(lines, "\n")
}

func (v *chatSearchView) summary() string {
	switch {
	case v.loading:
		return styleDim.Render("searching…")
	case v.asked == "":
		return ""
	case len(v.results) == 0:
		return styleDim.Render("no matches")
	case len(v.results) == 1:
		return styleDim.Render("1 chat")
	default:
		return styleDim.Render(fmt.Sprintf("%d chats", len(v.results)))
	}
}

func (v *chatSearchView) bodyLines(width, height int) []string {
	if height <= 0 {
		return nil
	}
	if v.errText != "" {
		return []string{" " + styleBad.Render(v.errText)}
	}
	if len(v.rows) == 0 {
		hint := "Type at least three characters. Every word must appear; matching ignores case and finds text inside identifiers."
		if v.asked != "" && !v.loading {
			hint = fmt.Sprintf("No chat mentions %q.", v.asked)
		}
		return styled(wrapText(hint, max(width-2, 1), wrapOpts{}), styleDim)
	}
	// Keep the cursor on screen.
	if v.cursor < v.top {
		v.top = v.cursor
	}
	if v.cursor >= v.top+height {
		v.top = v.cursor - height + 1
	}
	out := make([]string, 0, height)
	for i := v.top; i < len(v.rows) && len(out) < height; i++ {
		out = append(out, v.rowLine(i, width))
	}
	return out
}

func (v *chatSearchView) rowLine(i, width int) string {
	r := v.rows[i]
	res := &v.results[r.chat]
	selected := i == v.cursor
	mark := "  "
	if selected {
		mark = styleKey.Render("▌ ")
	}
	var line string
	if r.match < 0 {
		c := res.Chat
		where := "live"
		if chatstate.Terminal(chatstate.State(c.State)) {
			where = "archived"
		}
		tail := styleDim.Render(fmt.Sprintf("  #%d · %s · %s · %s", c.ID, chatStateLabel(c.State), where, c.Agent))
		if more := res.MatchCount - len(res.Matches); more > 0 {
			tail += styleDim.Render(fmt.Sprintf(" · %d more", more))
		}
		title := lipgloss.NewStyle().Bold(true).Render(c.Title)
		line = mark + applyStateStyle(c.State, chatStateGlyph(c.State)) + " " + title + tail
	} else {
		m := res.Matches[r.match]
		var where string
		switch m.Field {
		case apiclient.ChatSearchFieldPrompt:
			where = fmt.Sprintf("turn %d asked", m.Seq)
		case apiclient.ChatSearchFieldResult:
			where = fmt.Sprintf("turn %d answer", m.Seq)
		default:
			where = "title"
		}
		line = mark + "    " + styleDim.Render(fmt.Sprintf("%-15s", where)) + " " + snippetLine(m.Segments)
	}
	line = ansi.Truncate(line, max(width, 1), "…")
	if selected {
		line = lipgloss.NewStyle().Bold(true).Render(line)
	}
	return line
}

// snippetLine renders a snippet with its matching runs underlined and
// reversed — styling, not colour alone, so NO_COLOR still shows them (§15).
func snippetLine(segs []apiclient.ChatSearchSegment) string {
	hit := lipgloss.NewStyle().Underline(true).Reverse(true)
	var b strings.Builder
	for _, s := range segs {
		if s.Match {
			b.WriteString(hit.Render(s.Text))
		} else {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}
