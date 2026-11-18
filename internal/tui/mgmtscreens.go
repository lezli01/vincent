package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The management screens' wide layouts (task 150.7). Each is drawn over the
// view's existing model, so selection, forms, confirmations and fetches are the
// ones the narrow layout has always had; a terminal narrower than mgmtWideMinW
// keeps that layout.

// mgmtWideMinW is the narrowest terminal that gets the wide layouts.
const mgmtWideMinW = 130

// mgmtHit is where the last render drew one selectable card.
type mgmtHit struct{ x0, x1, y0, y1, index int }

// mgmtHitAt is the card under a left click.
func mgmtHitAt(hits []mgmtHit, msg tea.MouseClickMsg) (int, bool) {
	if msg.Button != tea.MouseLeft {
		return 0, false
	}
	for _, h := range hits {
		if msg.X >= h.x0 && msg.X < h.x1 && msg.Y >= h.y0 && msg.Y < h.y1 {
			return h.index, true
		}
	}
	return 0, false
}

// mgmtCard draws a card, thick-edged when selected.
func mgmtCard(body []string, w, h int, selected bool) []string {
	if selected {
		return drawBox("", "", body, w, h, edgesThick, styleFocus)
	}
	return drawBox("", "", body, w, h, edgesRounded, styleDim)
}

// mgmtFilterBox is the rounded box at the top of a management screen.
func mgmtFilterBox(filter *textField, typing bool, summary string, width int) []string {
	body := []string{toneBlue.Render("⌕ ") + styleDim.Render(filter.Placeholder())}
	if typing || filter.Value() != "" {
		filter.SetWidth(max(width-6, 10))
		body = fieldRows("", *filter)
	}
	return drawBox("", styleDim.Render(summary), body, width, len(body)+2, edgesRounded, styleDim)
}

// mgmtWindow is the first of n items to draw so that cursor is among the shown.
func mgmtWindow(n, cursor, shown int) int {
	return max(min(cursor-shown/2, n-shown), 0)
}

// Projects.

const (
	projectCardH    = 9
	projectCardMinW = 34
	attentionCardH  = 5
)

func (p *projectsView) wide() bool { return p.termW >= mgmtWideMinW && p.form == nil }

func (p *projectsView) frameless() bool { return p.wide() }

// renderWide is a grid of project cards beside everything that needs a person.
func (p *projectsView) renderWide(width, height int) string {
	rows := p.visible()
	summary := plural(len(rows), "project", "projects") + " · " + strconv.Itoa(len(p.attention())) + " need you"
	if p.infoOK {
		summary += " · " + strconv.Itoa(p.slotsUsed) + " of " + strconv.Itoa(p.globalCap) + " slots in use"
	}
	lines := mgmtFilterBox(&p.filter, p.filtering, summary, width)
	lines = append(lines, p.wideNotices(width)...)
	h := max(height-len(lines), 4)
	p.syncTable(rows, width, max(len(rows)+1, 2))

	paneW := min(width*30/100, 58)
	listW := width - paneW
	perRow := max((listW-4)/projectCardMinW, 1)
	p.perRow = perRow
	cardW := (listW - 4) / perRow
	body := p.projectGrid(rows, cardW, perRow, h-2, len(lines))
	if empty, ok := p.emptyBody(rows); ok {
		body = styled(wrapText(strings.TrimSpace(ansi.Strip(empty)), max(listW-4, 1), wrapOpts{}), styleDim)
	}
	left := drawBox(toneBlue.Bold(true).Render("◆ Projects"), toneBlue.Render(strconv.Itoa(len(rows))), body, listW, h, edgesRounded, styleDim)
	return strings.Join(append(lines, joinColumns(left, p.attentionPane(paneW, h))...), "\n")
}

// wideNotices is what must be said above the cards.
func (p *projectsView) wideNotices(width int) []string {
	var out []string
	if p.loadErr != nil {
		out = append(out, styleBad.Render(ansi.Truncate(" ⚠ refresh failed: "+errString(p.loadErr), width, "…")))
	}
	if p.err != "" {
		out = append(out, styleBad.Render(ansi.Truncate(" ⚠ "+p.err, width, "…")))
	}
	if p.confirm != nil {
		out = append(out, styleWarn.Render(" "+p.confirm.text+" ")+styleKey.Render("y")+styleDim.Render("/")+styleKey.Render("n"))
	}
	return out
}

// projectGrid is the cards, scrolled by whole rows to keep the selected one on
// screen.
func (p *projectsView) projectGrid(rows []apiclient.Project, cardW, perRow, room, top int) []string {
	p.hits = nil
	cursor := p.tbl.Cursor()
	total := (len(rows) + perRow - 1) / perRow
	shown := max(room/projectCardH, 1)
	start := mgmtWindow(total, max(cursor, 0)/perRow, shown)
	var out []string
	for r := start; r < min(start+shown, total); r++ {
		row := make([]string, projectCardH)
		for c := range perRow {
			i := r*perRow + c
			if i >= len(rows) {
				break
			}
			y := top + 1 + (r-start)*projectCardH
			p.hits = append(p.hits, mgmtHit{2 + c*cardW, 2 + (c+1)*cardW, y, y + projectCardH, i})
			row = joinColumns(row, p.projectCard(rows[i], cardW, i == cursor && !p.inAttention))
		}
		out = append(out, row...)
	}
	return out
}

// projectCard is one project's figures.
func (p *projectsView) projectCard(pr apiclient.Project, w int, selected bool) []string {
	inner := max(w-4, 1)
	head := padBetweenCells(toneMagenta.Bold(true).Render(headerProjectGlyph+" "+ansi.Truncate(oneLine(pr.Name), max(inner-12, 4), "…")),
		styleDim.Render(p.overviewCell(pr, ocActivity)), inner)
	stats := strings.Join([]string{
		styleWarn.Bold(true).Render(attentionBadge + " " + p.overviewCell(pr, ocAttention)),
		toneCyan.Render("▶ " + p.overviewCell(pr, ocRunning)),
		toneBlue.Render("◷ " + p.overviewCell(pr, ocQueued)),
		styleBad.Render("✗ " + p.overviewCell(pr, ocBlocked)),
		styleOK.Render("✓ " + p.overviewCell(pr, ocDone)),
	}, "   ")
	link := styleDim.Render("local only")
	if gh := p.githubCell(pr.ID); gh != overviewDash {
		link = toneCyan.Render("⇅ " + strings.TrimPrefix(gh, "✓ "))
	}
	body := []string{
		head, "", stats, "",
		"▦ " + p.overviewCell(pr, ocIssues) + styleDim.Render(" issues (imported)"),
		"◌ " + p.overviewCell(pr, ocChats) + styleDim.Render(" chats (waiting)"),
		link,
	}
	return mgmtCard(body, w, projectCardH, selected)
}

// attentionPane is every task waiting on a person, across projects.
func (p *projectsView) attentionPane(w, h int) []string {
	attn := p.attention()
	inner := max(w-4, 1)
	var body []string
	if len(attn) == 0 {
		body = []string{styleDim.Render("nothing needs you in any project")}
	}
	cursor := 0
	if p.inAttention {
		cursor = p.attnCursor
	}
	shown := max((h-2)/attentionCardH, 1)
	start := mgmtWindow(len(attn), cursor, shown)
	for i := start; i < min(start+shown, len(attn)); i++ {
		t := attn[i]
		status, style := taskCardStatus(t, p.now())
		body = append(body, mgmtCard([]string{
			toneBlue.Bold(true).Render(oneLine(t.ProjectName) + " #" + strconv.FormatInt(t.ID, 10)),
			ansi.Truncate(oneLine(t.Title), max(inner-4, 1), "…"),
			style.Render(status),
		}, inner, attentionCardH, p.inAttention && i == p.attnCursor)...)
	}
	title := styleWarn.Bold(true).Render(attentionBadge + " Needs you, across projects")
	return drawBox(title, styleWarn.Render(strconv.Itoa(len(attn))), body, w, h, edgesRounded, styleDim)
}

// moveProject moves the selection through the grid.
func (p *projectsView) moveProject(delta int) {
	n := len(p.visible())
	if n == 0 {
		return
	}
	moveCursorTo(&p.tbl, min(max(p.tbl.Cursor()+delta, 0), n-1))
	p.rememberSelection()
}

// Workflows draw their registry and graph in two panes of their own.

func (w *workflowsView) frameless() bool {
	return w.termW >= mgmtWideMinW && w.create == nil && w.editor == nil
}

// The daemon dashboard.

func (d *daemonView) wide() bool {
	return d.termW >= mgmtWideMinW && d.form == nil && d.statusLine == nil && d.skills == nil
}

func (d *daemonView) frameless() bool { return d.wide() }

// renderWide is the daemon as a dashboard: identity, configuration and database
// side by side, the adapters under them, and the log taking what is left.
func (d *daemonView) renderWide(width, height int) string {
	identity, config, database := d.identityLines()[1:], d.configLines()[1:], d.databaseLines()[1:]
	if d.doctorOK {
		database = append(database, issueArchiveLines(d.doctor.IssueArchive, width/3)...)
	}
	adapters := d.adapterLines()[1:]
	topH := min(max(len(identity), len(config), len(database))+2, max(height/2, 8))
	adapterH := min(len(adapters)+2, max(height/4, 4))
	logH := max(height-topH-adapterH, 5)

	w := width / 3
	configBorder := styleDim
	if d.focusConfig {
		configBorder = styleFocus
	}
	top := joinColumns(
		drawBox(styleTitle.Render("◉ Daemon"), styleDim.Render(fmt.Sprintf("up %s · pid %d", formatUptime(d.info.Uptime(d.now())), d.info.PID)), identity, w, topH, edgesRounded, styleDim),
		joinColumns(
			drawBox(styleTitle.Render("⚙ Config in effect"), "", config, w, topH, edgesRounded, configBorder),
			drawBox(styleTitle.Render("▤ Database"), "", database, width-2*w, topH, edgesRounded, styleDim),
		),
	)
	top = append(top, drawBox(styleTitle.Render("◈ Adapters"), "", adapters, width, adapterH, edgesRounded, styleDim)...)
	log := strings.Split(d.renderLog(max(logH-1, 2)), "\n")
	if len(log) > 1 {
		log = log[1:]
	}
	return strings.Join(append(top, drawBox(styleTitle.Render("≡ Recent log"), "", log, width, logH, edgesRounded, styleDim)...), "\n")
}

// Triggers.

const triggerCardH = 6

func (v *triggersView) wide() bool {
	return v.termW >= mgmtWideMinW && v.create == nil && v.dry == nil && v.form == nil
}

func (v *triggersView) frameless() bool { return v.wide() }

// renderWide is the triggers as cards, with the selected one's details beside
// them.
func (v *triggersView) renderWide(width, height int) string {
	rows := v.visible()
	lines := mgmtFilterBox(&v.filter, v.filtering, plural(len(rows), "trigger", "triggers")+" · "+v.list.Dir, width)
	lines = append(lines, v.bannerLines(width)...)
	if v.loadErr != nil {
		lines = append(lines, " "+styleBad.Render("could not read the triggers: "+errString(v.loadErr)))
	}
	if v.note != "" {
		lines = append(lines, " "+styleOK.Render(v.note))
	}
	if v.err != "" {
		lines = append(lines, " "+styleBad.Render("⚠ "+v.err))
	}
	lines = truncateRows(lines, width)
	h := max(height-len(lines), 4)
	listW := min(width*34/100, 66)
	inner := max(listW-4, 1)
	v.hits = nil
	var cards []string
	shown := max((h-2)/triggerCardH, 1)
	start := mgmtWindow(len(rows), v.cursor, shown)
	for i := start; i < min(start+shown, len(rows)); i++ {
		y := len(lines) + 1 + (i-start)*triggerCardH
		v.hits = append(v.hits, mgmtHit{2, 2 + inner, y, y + triggerCardH, i})
		cards = append(cards, v.triggerCard(rows[i], inner, i == v.cursor)...)
	}
	if len(rows) == 0 {
		cards = []string{styleDim.Render("no triggers here — a creates one, and it starts disabled")}
	}
	left := drawBox(toneBlue.Bold(true).Render("⚡ Triggers"), toneBlue.Render(strconv.Itoa(len(rows))), cards, listW, h, edgesRounded, styleDim)

	detailW := width - listW
	body := v.detailLines(detailW - 4)
	if len(rows) > 0 {
		body = append(body, v.ledgerLines(detailW-4, max(h-2-len(body), trigLedgerMinHeight))...)
	}
	s, _ := v.current()
	right := drawBox(toneBlue.Bold(true).Render(oneLine(s.ID)), "", body, detailW, h, edgesRounded, styleFocus)
	return strings.Join(append(lines, joinColumns(left, right)...), "\n")
}

// triggerCard is one trigger's state at a glance.
func (v *triggersView) triggerCard(s apiclient.TriggerSummary, w int, selected bool) []string {
	inner := max(w-4, 1)
	armed, armStyle := trigArmed(s)
	flow := "—"
	if s.Valid {
		flow = s.SourceType + " → " + s.ActionType + " · " + firstNonEmpty(s.OnFire, "—")
	}
	poll, pollStyle := trigPollCell(s)
	return mgmtCard([]string{
		padBetweenCells(toneMagenta.Bold(true).Render("⚡ "+ansi.Truncate(oneLine(s.ID), max(inner-16, 4), "…")), armStyle.Bold(true).Reverse(true).Render(" "+armed+" "), inner),
		flow, "",
		padBetweenCells(pollStyle.Render("poll "+poll)+styleDim.Render(" · "+v.ago(s.Poll.LastPollAt)), styleDim.Render("fired "+v.ago(s.Poll.LastFireAt)), inner),
	}, w, triggerCardH, selected)
}

// Pull requests.

const pullCardH = 6

// Pull request columns.
const (
	pullColumnLinked = iota
	pullColumnUnclaimed
	pullColumnCount
)

func (v *pullRequestsView) wide() bool {
	return v.termW >= mgmtWideMinW && v.picker == nil && v.usable() && v.loaded && v.loadErr == "" && len(v.pulls) > 0
}

func (v *pullRequestsView) frameless() bool { return v.wide() }

// pullLaneRows is each column's indices into rows().
func (v *pullRequestsView) pullLaneRows() [pullColumnCount][]int {
	var out [pullColumnCount][]int
	for i, r := range v.rows() {
		c := pullColumnUnclaimed
		if r.pull.TaskID != nil {
			c = pullColumnLinked
		}
		out[c] = append(out[c], i)
	}
	return out
}

// pullPosition is the column and height the cursor is at.
func pullPosition(cols [pullColumnCount][]int, cursor int) (column, pos int) {
	for c := range cols {
		for p, i := range cols[c] {
			if i == cursor {
				return c, p
			}
		}
	}
	return 0, 0
}

// movePullCard moves the selection within its column.
func (v *pullRequestsView) movePullCard(delta int) {
	cols := v.pullLaneRows()
	c, p := pullPosition(cols, v.cursor)
	if len(cols[c]) > 0 {
		v.cursor = cols[c][min(max(p+delta, 0), len(cols[c])-1)]
	}
}

// movePullColumn crosses to the other column at the same height.
func (v *pullRequestsView) movePullColumn(delta int) {
	cols := v.pullLaneRows()
	c, p := pullPosition(cols, v.cursor)
	if n := c + delta; n >= 0 && n < pullColumnCount && len(cols[n]) > 0 {
		v.cursor = cols[n][min(p, len(cols[n])-1)]
	}
}

// renderWide is the pull requests by whether a task claims them, with the
// selected one previewed.
func (v *pullRequestsView) renderWide(width, height int) string {
	rows := v.rows()
	summary := strconv.Itoa(len(rows)) + " " + v.state + " · updated " + v.lastLoad.Format("15:04:05")
	lines := mgmtFilterBox(&v.filter, v.filtering, summary, width)
	if v.confirm != nil {
		lines = append(lines, styleWarn.Render(ansi.Truncate(" "+v.confirm.text+" (y/n)", width, "…")))
	} else if v.note != "" {
		style := styleDim
		if v.noteBad {
			style = styleBad
		}
		lines = append(lines, style.Render(ansi.Truncate(" "+v.note, width, "…")))
	}
	h := max(height-len(lines), 4)
	paneW := min(width*30/100, 58)
	laneW := (width - paneW) / pullColumnCount
	cols := v.pullLaneRows()
	v.hits = nil
	left := v.pullLane(toneCyan.Bold(true).Render("▶ Linked to a task"), rows, cols[pullColumnLinked], 0, len(lines), laneW, h)
	right := v.pullLane(styleDim.Render("○ Unclaimed"), rows, cols[pullColumnUnclaimed], laneW, len(lines), width-paneW-laneW, h)
	return strings.Join(append(lines, joinColumns(joinColumns(left, right), v.pullPreview(paneW, h))...), "\n")
}

// pullLane draws one column of pull request cards.
func (v *pullRequestsView) pullLane(title string, rows []prRow, idx []int, x, top, w, h int) []string {
	inner := max(w-4, 1)
	var body []string
	shown := max((h-2)/pullCardH, 1)
	_, pos := pullPosition([pullColumnCount][]int{idx}, v.cursor)
	start := mgmtWindow(len(idx), pos, shown)
	for n := start; n < min(start+shown, len(idx)); n++ {
		i := idx[n]
		y := top + 1 + (n-start)*pullCardH
		v.hits = append(v.hits, mgmtHit{x + 2, x + 2 + inner, y, y + pullCardH, i})
		body = append(body, v.pullCard(rows[i].pull, inner, i == v.cursor)...)
	}
	if len(idx) == 0 {
		body = []string{styleDim.Render("none")}
	}
	return drawBox(title, styleDim.Render(strconv.Itoa(len(idx))), body, w, h, edgesRounded, styleDim)
}

// pullStatePill is a pull request's state as a filled label.
func pullStatePill(p apiclient.GitHubPullRequest) string {
	style := styleOK
	switch {
	case p.Draft, p.State == "closed" && !p.Merged:
		style = styleDim
	case p.Merged:
		style = toneMagenta
	}
	return style.Bold(true).Reverse(true).Render(" " + p.Status() + " ")
}

// pullCard is one pull request.
func (v *pullRequestsView) pullCard(p apiclient.GitHubPullRequest, w int, selected bool) []string {
	inner := max(w-4, 1)
	link := styleDim.Render("no task")
	if p.TaskID != nil {
		link = toneCyan.Render("▶ task #" + strconv.FormatInt(*p.TaskID, 10))
	}
	return mgmtCard([]string{
		padBetweenCells(styleKey.Render("#"+strconv.Itoa(p.Number))+"  "+pullStatePill(p), styleDim.Render(issueAge(v.now(), p.UpdatedAt)), inner),
		ansi.Truncate(oneLine(p.Title), inner, "…"),
		toneCyan.Render(ansi.Truncate("⎇ "+oneLine(p.HeadBranch)+" → "+oneLine(p.BaseBranch), inner, "…")),
		link,
	}, w, pullCardH, selected)
}

// pullPreview is the selected pull request in more detail.
func (v *pullRequestsView) pullPreview(w, h int) []string {
	row, ok := v.current()
	if !ok {
		return drawBox(styleTitle.Render("Preview"), "", []string{styleDim.Render("Nothing matches the filter.")}, w, h, edgesRounded, styleDim)
	}
	p := row.pull
	inner := max(w-4, 1)
	body := styled(wrapText(oneLine(p.Title), inner, wrapOpts{maxLines: 3}), styleTitle)
	body = append(body, "", pullStatePill(p), "")
	if p.Author != "" {
		body = append(body, styleDim.Render("by "+oneLine(p.Author)))
	}
	body = append(body, toneCyan.Render(ansi.Truncate("⎇ "+oneLine(p.HeadBranch), inner, "…")), "", styleTitle.Render("✓ Checks"))
	body = append(body, v.pullCheckLines(p, inner)...)
	if p.TaskID != nil {
		task := "#" + strconv.FormatInt(*p.TaskID, 10)
		state := ""
		if t, found := v.taskByID(*p.TaskID); found {
			task += " " + oneLine(t.Title)
			state = stateWords(t.State)
		}
		body = append(body, "", styleTitle.Render("▶ Task"), ansi.Truncate(task, inner, "…"), styleDim.Render(state))
	}
	return drawBox(styleKey.Render("#"+strconv.Itoa(p.Number)), "", body, w, h, edgesRounded, styleDim)
}

// pullCheckLines is the selected pull request's checks. The daemon reads checks
// through the task that claims a pull request, so an unclaimed one has none.
func (v *pullRequestsView) pullCheckLines(p apiclient.GitHubPullRequest, inner int) []string {
	if p.TaskID == nil {
		return []string{styleDim.Render("Link a task to read its checks.")}
	}
	held, ok := v.checks[*p.TaskID]
	switch {
	case !ok:
		return []string{styleDim.Render("Loading…")}
	case held.err != "":
		return styled(wrapText(held.err, inner, wrapOpts{maxLines: 2}), styleWarn)
	case len(held.runs) == 0:
		return []string{styleDim.Render("No checks reported.")}
	}
	out := make([]string, 0, len(held.runs))
	for _, r := range held.runs {
		glyph, style := "○", styleDim
		switch r.State {
		case "success":
			glyph, style = "✓", styleOK
		case "failure", "timed_out", "cancelled", "action_required":
			glyph, style = "✗", styleBad
		case "in_progress":
			glyph, style = "●", toneCyan
		}
		out = append(out, style.Render(glyph+" ")+ansi.Truncate(oneLine(r.Name), max(inner-2, 1), "…"))
	}
	return out
}

// pullChecks is one read of a task's checks.
type pullChecks struct {
	runs []apiclient.GitHubCheckRun
	err  string
}

type (
	// pullChecksDueMsg says the selection may have rested on a task long enough.
	pullChecksDueMsg struct{ taskID int64 }
	// pullChecksMsg is the checks read for a task.
	pullChecksMsg struct {
		taskID int64
		runs   []apiclient.GitHubCheckRun
		err    error
	}
)

// checksCmd arms the read of the selected pull request's checks: one read per
// pull request the selection rests on, however quickly it passes the others.
func (v *pullRequestsView) checksCmd() tea.Cmd {
	if !v.wide() || v.client == nil {
		return nil
	}
	row, ok := v.current()
	if !ok || row.pull.TaskID == nil {
		return nil
	}
	id := *row.pull.TaskID
	if _, held := v.checks[id]; held || v.checksArmed == id {
		return nil
	}
	v.checksArmed = id
	return tea.Tick(taskPreviewDebounce, func(time.Time) tea.Msg { return pullChecksDueMsg{id} })
}

// checksDue reads the checks if the selection is still on that task's pull
// request.
func (v *pullRequestsView) checksDue(id int64) tea.Cmd {
	v.checksArmed = 0
	row, ok := v.current()
	if !ok || row.pull.TaskID == nil || *row.pull.TaskID != id || v.client == nil {
		return nil
	}
	client := v.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		checks, err := client.TaskGitHubChecks(ctx, id)
		if err == nil && checks.Reason != "" {
			err = errors.New(checks.Reason)
		}
		return pullChecksMsg{taskID: id, runs: checks.Runs, err: err}
	}
}

// applyChecks keeps a read's answer until the list is next refreshed.
func (v *pullRequestsView) applyChecks(msg pullChecksMsg) {
	if v.checks == nil {
		v.checks = map[int64]pullChecks{}
	}
	held := pullChecks{runs: msg.runs}
	if msg.err != nil {
		held.err = githubReasonMessage(msg.err)
	}
	v.checks[msg.taskID] = held
}
