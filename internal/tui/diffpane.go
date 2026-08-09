package tui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

const (
	// diffTimeout bounds the fetch: the endpoint shells out to git, which is
	// fast on a worktree and not instant on a large one.
	diffTimeout = 20 * time.Second
	// maxDiffLines caps what the pane renders. §18 allows an agent to touch a
	// vendored tree; the endpoint still served the whole diff and T4.3 owns
	// real limits, so this bounds the terminal, not the truth.
	maxDiffLines = 5000
)

var (
	styleDiffFile = lipgloss.NewStyle().Bold(true)
	styleDiffHunk = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleDiffAdd  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleDiffDel  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

// diffLoadedMsg carries a fetched diff.
type diffLoadedMsg struct {
	taskID int64
	text   string
	err    error
}

// diffPane is the §15 diff tab: the worktree against merge-base with the base
// branch. It fetches when the tab opens and on an explicit refresh, never
// from the event stream — the endpoint runs git per call, so following events
// would mean a subprocess per event.
type diffPane struct {
	taskID    int64
	lines     []string
	loaded    bool
	fetching  bool
	err       error
	truncated bool

	vp         viewport.Model
	built      bool
	builtWidth int
}

func newDiffPane() diffPane {
	return diffPane{vp: viewport.New()}
}

// open points the pane at a task, discarding another task's diff.
func (p *diffPane) open(taskID int64) {
	if p.taskID == taskID {
		return
	}
	p.taskID = taskID
	p.reset()
}

func (p *diffPane) reset() {
	p.lines = nil
	p.loaded = false
	p.fetching = false
	p.err = nil
	p.truncated = false
	p.built = false
	p.vp.SetContent("")
}

// fetch reloads the diff. stale=false means "only if we have nothing", which
// is what opening the tab asks for; a refresh keypress passes true.
func (p *diffPane) fetch(client *apiclient.Client, force bool) tea.Cmd {
	if client == nil || p.taskID == 0 || p.fetching {
		return nil
	}
	if p.loaded && !force {
		return nil
	}
	p.fetching = true
	id := p.taskID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
		defer cancel()
		text, err := client.Diff(ctx, id)
		return diffLoadedMsg{taskID: id, text: text, err: err}
	}
}

func (p *diffPane) apply(msg diffLoadedMsg) {
	if msg.taskID != p.taskID {
		return
	}
	p.fetching = false
	if msg.err != nil {
		p.err = msg.err
		return
	}
	p.err = nil
	p.loaded = true
	p.lines = splitDiff(msg.text)
	p.truncated = len(p.lines) > maxDiffLines
	if p.truncated {
		p.lines = p.lines[:maxDiffLines]
	}
	p.built = false
}

func splitDiff(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

func (p *diffPane) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.vp, cmd = p.vp.Update(msg)
	return cmd
}

// render draws the diff, or the reason there is none. The endpoint's two
// conflicts are different situations: a task that has not started has no
// worktree yet, while an archived one had its worktree removed on purpose —
// and only the first is worth waiting on.
func (p *diffPane) render(width, height int) string {
	if body, ok := p.emptyState(); ok {
		return styleDim.Render("  " + body)
	}
	p.vp.SetWidth(max(width, 1))
	p.vp.SetHeight(max(height, 1))
	if !p.built || p.builtWidth != width {
		p.vp.SetContent(strings.Join(p.renderLines(), "\n"))
		p.built = true
		p.builtWidth = width
	}
	return p.vp.View()
}

func (p *diffPane) emptyState() (string, bool) {
	switch {
	case p.err != nil:
		return p.errorText(), true
	case p.fetching && len(p.lines) == 0:
		return "loading diff…", true
	case !p.loaded:
		return "press d to load the diff", true
	case len(p.lines) == 0:
		return "no changes against the base branch yet", true
	default:
		return "", false
	}
}

// errorText explains a refusal in the terms of the task's own life cycle.
func (p *diffPane) errorText() string {
	var apiErr *apiclient.Error
	if errors.As(p.err, &apiErr) && apiErr.Status == http.StatusConflict {
		switch {
		case strings.Contains(apiErr.Message, "no worktree"):
			return "no worktree yet — this task has not started running"
		case strings.Contains(apiErr.Message, "no longer exists"):
			return "the worktree was removed (archived) — the branch still holds the commits"
		default:
			return "diff unavailable: " + apiErr.Message
		}
	}
	return "diff failed: " + errString(p.err)
}

// renderLines colours the structure of the diff — files, hunks, additions,
// removals. Per-language highlighting inside a diff would mean stripping the
// gutter and detecting a lexer per file, which is a different feature.
func (p *diffPane) renderLines() []string {
	out := make([]string, 0, len(p.lines)+1)
	for _, line := range p.lines {
		out = append(out, colorDiffLine(line))
	}
	if p.truncated {
		out = append(out, styleDim.Render("  … diff truncated; the whole change is on the branch"))
	}
	return out
}

// diffClass is what one line of a unified diff is.
type diffClass int

const (
	diffContext diffClass = iota
	diffFile
	diffHunk
	diffAdd
	diffDel
)

// classifyDiffLine reads the structure. The ± file markers are checked before
// the ± content prefixes: they carry the same first character, and colouring
// them as changes makes every file header read as one added and one removed
// line.
func classifyDiffLine(line string) diffClass {
	switch {
	case strings.HasPrefix(line, "diff --git"), strings.HasPrefix(line, "index "),
		strings.HasPrefix(line, "new file"), strings.HasPrefix(line, "deleted file"),
		strings.HasPrefix(line, "similarity index"), strings.HasPrefix(line, "rename "),
		strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		return diffFile
	case strings.HasPrefix(line, "@@"):
		return diffHunk
	case strings.HasPrefix(line, "+"):
		return diffAdd
	case strings.HasPrefix(line, "-"):
		return diffDel
	default:
		return diffContext
	}
}

func colorDiffLine(line string) string {
	switch classifyDiffLine(line) {
	case diffFile:
		return styleDiffFile.Render(line)
	case diffHunk:
		return styleDiffHunk.Render(line)
	case diffAdd:
		return styleDiffAdd.Render(line)
	case diffDel:
		return styleDiffDel.Render(line)
	case diffContext:
		return line
	}
	return line
}
