package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The triggers view's rendering. The list is the daemon view's shape: a table
// on top, the selected row's facts under it, and the ledger pane taking what
// is left, with tab deciding which of the two lists the arrows mean.

const (
	trigLedgerMinHeight = 4
	trigDetailLines     = 4
)

func (v *triggersView) render(width, height int) string {
	if width > 0 {
		v.width = width
	}
	if height > 0 {
		v.height = height
	}
	switch {
	case v.create != nil:
		return v.renderCreate(v.width, v.height)
	case v.dry != nil:
		return v.renderDry(v.width, v.height)
	case v.form != nil:
		return v.renderForm(v.width, v.height)
	}
	return v.renderList(v.width, v.height)
}

// bannerLines is the global-off banner: every trigger is inert while
// `triggers.enabled` is off, whatever its own enabled says.
func (v *triggersView) bannerLines() []string {
	if !v.loaded || v.list.Enabled {
		return nil
	}
	return []string{
		" " + styleWarn.Render("⚠ triggers.enabled is off in config.yaml — no trigger polls, accepts a push or fires, whatever its own enabled says"),
		"   " + styleDim.Render("B opens the switch in the daemon view's config editor, which asks before turning it on"),
	}
}

func (v *triggersView) renderList(width, height int) string {
	head := []string{" " + styleTitle.Render("triggers") + styleDim.Render("   "+v.list.Dir)}
	head = append(head, v.bannerLines()...)
	if v.filtering || v.filter.Value() != "" {
		v.filter.SetWidth(max(width-2, 10))
		head = append(head, fieldRows(" ", v.filter)...)
	}
	if v.loadErr != nil {
		head = append(head, " "+styleBad.Render("could not read the triggers: "+errString(v.loadErr)))
	}
	if v.note != "" {
		head = append(head, " "+styleOK.Render(v.note))
	}
	if v.err != "" {
		head = append(head, " "+styleBad.Render("⚠ "+v.err))
	}
	head = append(head, "")
	if !v.loaded {
		return clampLines(append(head, styleDim.Render("  reading the triggers…")), height)
	}
	rows := v.visible()
	switch {
	case len(v.list.Triggers) == 0:
		return clampLines(append(head, styleDim.Render(
			"  no trigger files yet — a creates one, and it starts disabled")), height)
	case len(rows) == 0:
		return clampLines(append(head, styleDim.Render("  nothing matches the filter — esc clears it")), height)
	}

	detail := v.detailLines(width)
	budget := height
	if budget <= 0 {
		budget = len(head) + len(rows) + 1 + len(detail) + 12
	}
	tableHeight := min(len(rows)+1, max((budget-len(head)-len(detail))/2, 3))
	table := v.tableLines(rows, width, tableHeight)
	ledgerHeight := max(budget-len(head)-len(table)-len(detail)-1, trigLedgerMinHeight)

	out := make([]string, 0, len(head)+len(table)+1+len(detail)+ledgerHeight)
	out = append(out, head...)
	out = append(out, table...)
	out = append(out, "")
	out = append(out, detail...)
	out = append(out, v.ledgerLines(width, ledgerHeight)...)
	return clampLines(out, height)
}

// trigCell pads a cell to its column after truncating it, then styles it, so
// the escape codes never count towards the width.
func trigCell(text string, w int, style lipgloss.Style) string {
	text = ansi.Truncate(text, w, "…")
	return style.Render(text + strings.Repeat(" ", max(w-ansi.StringWidth(text), 0)))
}

func (v *triggersView) tableLines(rows []apiclient.TriggerSummary, width, height int) []string {
	idW, projW := len("ID"), len("PROJECT")
	for _, s := range rows {
		idW = max(idW, ansi.StringWidth(s.ID))
		projW = max(projW, ansi.StringWidth(v.projectName(s.ProjectID)))
	}
	idW, projW = min(idW, 28), min(projW, 18)
	const (
		enW, armW, kindW, fireW, pollW, agoW = 7, 12, 26, 7, 7, 10
	)
	gap := "  "
	header := "  " + trigCell("ID", idW, styleDim) + gap + trigCell("ENABLED", enW, styleDim) + gap +
		trigCell("ARMED", armW, styleDim) + gap + trigCell("SOURCE → ACTION", kindW, styleDim) + gap +
		trigCell("PROJECT", projW, styleDim) + gap + trigCell("ON FIRE", fireW, styleDim) + gap +
		trigCell("POLL", pollW, styleDim) + gap + trigCell("LAST POLL", agoW, styleDim) + gap +
		styleDim.Render("LAST FIRE")
	lines := make([]string, 0, len(rows))
	for i, s := range rows {
		mark, idStyle := "  ", lipgloss.NewStyle()
		if i == v.cursor {
			mark, idStyle = styleFocus.Render("▸ "), styleTitle
			if v.focus == trigFocusLedger {
				mark = styleDim.Render("▸ ")
			}
		}
		enabled, enStyle := "off", styleDim
		if s.Enabled {
			enabled, enStyle = "on", styleOK
		}
		armed, armStyle := trigArmed(s)
		kind := "—"
		if s.Valid {
			kind = s.SourceType + " → " + s.ActionType
		}
		poll, pollStyle := trigPollCell(s)
		line := mark + trigCell(s.ID, idW, idStyle) + gap + trigCell(enabled, enW, enStyle) + gap +
			trigCell(armed, armW, armStyle) + gap + trigCell(kind, kindW, lipgloss.NewStyle()) + gap +
			trigCell(v.projectName(s.ProjectID), projW, styleDim) + gap +
			trigCell(firstNonEmpty(s.OnFire, "—"), fireW, trigOnFireStyle(s.OnFire)) + gap +
			trigCell(poll, pollW, pollStyle) + gap +
			trigCell(v.ago(s.Poll.LastPollAt), agoW, styleDim) + gap +
			styleDim.Render(v.ago(s.Poll.LastFireAt))
		lines = append(lines, line)
	}
	body := window(lines, v.cursor, max(height-1, 1))
	return truncateRows(append([]string{header}, body...), width)
}

// trigArmed is the armed cell: armed, or the disarmed reason in a word.
func trigArmed(s apiclient.TriggerSummary) (string, lipgloss.Style) {
	switch {
	case s.Armed:
		return "● armed", styleOK
	case !s.Valid:
		return "✗ invalid", styleBad
	case !s.Enabled:
		return "disabled", styleDim
	case strings.Contains(s.DisarmedReason, "triggers.enabled"):
		return "global off", styleWarn
	}
	return firstNonEmpty(s.DisarmedReason, "disarmed"), styleWarn
}

// trigPollCell is poll health. A pushed source has no poll, and a trigger
// that has not polled yet has no health to report.
func trigPollCell(s apiclient.TriggerSummary) (string, lipgloss.Style) {
	switch {
	case !s.Valid:
		return "—", styleDim
	case s.SourceType == "http":
		return "push", styleDim
	case s.Poll.LastPollAt == nil:
		return "not yet", styleDim
	case s.Poll.OK:
		return "ok", styleOK
	}
	return "failing", styleBad
}

func trigOnFireStyle(onFire string) lipgloss.Style {
	if onFire == "create" {
		return styleWarn
	}
	return styleDim
}

// ago renders a daemon timestamp relative to now, "—" for none.
func (v *triggersView) ago(ts *string) string {
	if ts == nil {
		return "—"
	}
	t, err := time.Parse(time.RFC3339Nano, *ts)
	if err != nil {
		return *ts
	}
	return formatElapsed(max(v.now().Sub(t), 0).Truncate(time.Second)) + " ago"
}

// detailLines are the selected trigger's facts the table has no room for,
// or the question in front of a write when one is open.
func (v *triggersView) detailLines(width int) []string {
	if c := v.confirm; c != nil {
		out := []string{" " + styleWarn.Render("? "+c.question)}
		for _, w := range c.warning {
			out = append(out, "   "+styleWarn.Render("⚠ "+w))
		}
		out = append(out, "   "+styleKey.Render("y")+styleDim.Render(" yes   ")+
			styleKey.Render("n")+styleDim.Render("/")+styleKey.Render("esc")+styleDim.Render(" leave it as it is"))
		return append(truncateRows(out, width), "")
	}
	s, ok := v.current()
	if !ok {
		return nil
	}
	out := []string{workflowFact("file", s.File)}
	switch {
	case s.Armed && !s.Poll.Seeded && s.SourceType != "http":
		out = append(out, workflowFact("armed", styleOK.Render("yes")+styleDim.Render(" — its next poll seeds and fires nothing")))
	case s.Armed:
		out = append(out, workflowFact("armed", styleOK.Render("yes")))
	default:
		out = append(out, workflowFact("armed", styleDim.Render("no — "+firstNonEmpty(s.DisarmedReason, "disarmed"))))
	}
	if s.Poll.Error != "" {
		out = append(out, workflowFact("poll error", styleBad.Render(s.Poll.Error)))
	}
	for _, e := range s.Errors {
		if len(out) >= trigDetailLines {
			break
		}
		out = append(out, workflowFact("invalid", styleBad.Render(findingText(e))))
	}
	return append(truncateRows(out, width), "")
}

func (v *triggersView) ledgerLines(width, height int) []string {
	s, _ := v.current()
	title := " " + styleTitle.Render("deliveries · "+s.ID)
	if v.focus == trigFocusLedger {
		title = styleFocus.Render("›") + styleTitle.Render("deliveries · "+s.ID) +
			styleDim.Render("   ↑/↓ select   enter open the task   tab back to the list")
	} else {
		title += styleDim.Render("   tab to select one")
	}
	out := []string{title}
	switch {
	case v.ledgerErr != nil:
		return append(out, "   "+styleBad.Render("could not read the ledger: "+errString(v.ledgerErr)))
	case v.ledgerID != s.ID:
		return append(out, "   "+styleDim.Render("reading the ledger…"))
	case len(v.ledger) == 0:
		return append(out, "   "+styleDim.Render("no deliveries yet — every event this trigger judges is recorded here, fired or not"))
	}
	out = append(out, "  "+styleDim.Render(fmt.Sprintf("%-10s  %-12s  %-24s  %-6s  %s", "WHEN", "OUTCOME", "EVENT", "TASK", "DETAIL")))
	rows := make([]string, 0, len(v.ledger))
	for i, d := range v.ledger {
		mark := "  "
		if v.focus == trigFocusLedger && i == v.ledgerCursor {
			mark = styleFocus.Render("▸ ")
		}
		task := "—"
		if d.TaskID != nil {
			task = "#" + strconv.FormatInt(*d.TaskID, 10)
		}
		when := d.CreatedAt
		rows = append(rows, mark+trigCell(v.ago(&when), 10, styleDim)+"  "+
			trigCell(d.Outcome, 12, trigOutcomeStyle(d.Outcome))+"  "+
			trigCell(d.EventID, 24, lipgloss.NewStyle())+"  "+
			trigCell(task, 6, lipgloss.NewStyle())+"  "+styleDim.Render(d.Detail))
	}
	cursor := 0
	if v.focus == trigFocusLedger {
		cursor = v.ledgerCursor
	}
	out = append(out, window(rows, cursor, max(height-2, 1))...)
	return truncateRows(out, width)
}

func trigOutcomeStyle(outcome string) lipgloss.Style {
	switch outcome {
	case apiclient.TriggerFired:
		return styleOK
	case apiclient.TriggerRateLimited:
		return styleWarn
	case apiclient.TriggerRefused, apiclient.TriggerError:
		return styleBad
	}
	return styleDim
}

func (v *triggersView) renderForm(width, height int) string {
	f := v.form
	out := []string{
		styleTitle.Render("  trigger " + f.id),
		"  " + styleDim.Render(f.file),
	}
	if f.path != "" {
		out = append(out, "  "+styleDim.Render("in "+f.path+" — esc goes back up"))
	}
	if v.loaded && !v.list.Enabled {
		out = append(out, "  "+styleWarn.Render("triggers.enabled is off — nothing here polls or fires until it is on"))
	}
	out = append(out, "")
	switch {
	case f.overlay != nil && f.overlay.FullPane():
		out = append(out, f.overlay.View(width, height))
	case f.loading && f.def == nil:
		out = append(out, styleDim.Render("  loading the schema and the file…"))
	case f.def != nil:
		out = append(out, renderFormRows(f.plainRows(), f.cursor, f.editing, f.input, f.overlay, width, v.rowNote(f))...)
	}
	if f.saving {
		out = append(out, "", styleDim.Render("  saving…"))
	}
	if f.note != "" {
		out = append(out, "", "  "+styleOK.Render(f.note))
	}
	if f.err != "" {
		out = append(out, "", "  "+styleBad.Render(f.err))
	}
	switch {
	case f.confirm != nil:
		out = append(out, "", "  "+styleWarn.Render("? set "+f.confirm.row.path+" to "+f.confirm.value+"?"))
		for _, w := range f.confirm.warning {
			out = append(out, "    "+styleWarn.Render("⚠ "+w))
		}
		out = append(out, "    "+styleDim.Render("y write it · n/esc keep the file as it is"))
	case f.overlay != nil:
	case f.input != nil:
		out = append(out, "", styleDim.Render("  enter commit · esc cancel"))
	default:
		out = append(out, "", styleDim.Render("  enter edit · "+opKey(keymap.Refresh)+" reload · esc back · "+opKey(keymap.Editor)+" $EDITOR from the list"))
	}
	return clampLines(truncateRows(out, width), height)
}

func (v *triggersView) renderCreate(width, height int) string {
	f := v.create
	mark := func(row int) string {
		if f.row == row {
			return styleFocus.Render("› ")
		}
		return "  "
	}
	label := func(s string) string { return styleDim.Render(fmt.Sprintf("%-12s", s)) + " " }
	fieldW := max(width-17, 10)
	out := []string{styleTitle.Render("  New trigger"), ""}
	f.id.SetWidth(fieldW)
	out = append(out, indentRows(mark(trigCreateRowID)+label("id"), f.id.rows())...)
	projects := make([]string, 0, len(f.projects))
	for i, p := range f.projects {
		if i == f.project {
			projects = append(projects, styleFocus.Render("["+p.Name+"]"))
			continue
		}
		projects = append(projects, styleDim.Render(" "+p.Name+" "))
	}
	if len(projects) == 0 {
		projects = append(projects, styleBad.Render("no project is registered"))
	}
	out = append(out, mark(trigCreateRowProject)+label("project")+strings.Join(projects, " "))
	f.command.SetWidth(fieldW)
	out = append(out, indentRows(mark(trigCreateRowCommand)+label("command"), f.command.rows())...)
	f.interval.SetWidth(fieldW)
	out = append(out, indentRows(mark(trigCreateRowInterval)+label("poll every"), f.interval.rows())...)
	out = append(out, "", styleDim.Render(
		"  writes a type: command trigger, disabled and with no on_fire line (so propose); the form opens on it next"))
	if f.saving {
		out = append(out, "", styleDim.Render("  writing…"))
	}
	if f.err != "" {
		out = append(out, "", "  "+styleBad.Render(f.err))
	}
	out = append(out, "", styleDim.Render("  tab row · ←→ project · enter create · esc cancel"))
	return clampLines(truncateRows(out, width), height)
}

func (v *triggersView) renderDry(width, height int) string {
	d := v.dry
	var out []string
	if d.poll {
		out = append(out, styleTitle.Render("  dry run · "+d.id+" — the source, run once for real"),
			"  "+styleDim.Render("nothing fires, and no cursor, ledger row or poll health changes"), "")
		switch {
		case d.running:
			out = append(out, styleDim.Render("  running the source…"))
		case d.result != nil:
			out = append(out, v.pollLines(*d.result)...)
		}
		if d.err != "" {
			out = append(out, "", "  "+styleBad.Render(d.err))
		}
		out = append(out, "", styleDim.Render("  ctrl+s run it again · esc close"))
		return clampLines(truncateRows(out, width), height)
	}
	out = append(out, styleTitle.Render("  dry run · "+d.id+" — judge a sample event"),
		"  "+styleDim.Render("nothing fires; the sample is kept for this session only and never written to disk"), "")
	paneHeight := min(12, max(height/3, 4))
	d.pane.SetSize(max(width-4, 10), paneHeight)
	out = append(out, indentRows("  ", strings.Split(d.pane.View(), "\n"))...)
	out = append(out, styleDim.Render("  ctrl+s judge it · esc close"), "")
	switch {
	case d.running:
		out = append(out, styleDim.Render("  judging…"))
	case d.judgement != nil:
		out = append(out, judgementLines(*d.judgement)...)
	}
	if d.err != "" {
		out = append(out, "", "  "+styleBad.Render(d.err))
	}
	return clampLines(truncateRows(out, width), height)
}

func (v *triggersView) pollLines(p apiclient.TriggerDryPoll) []string {
	var out []string
	if p.Error != "" {
		out = append(out, workflowFact("source", styleBad.Render("failed: "+p.Error)))
	}
	summary := fmt.Sprintf("%d events", len(p.Events))
	if p.Truncated > 0 {
		summary += fmt.Sprintf(" · %d over the catch-up cap", p.Truncated)
	}
	if p.Refused > 0 {
		summary += fmt.Sprintf(" · %d lines refused", p.Refused)
	}
	out = append(out, workflowFact("returned", summary))
	if p.Cursor != nil {
		out = append(out, workflowFact("cursor", strconv.Quote(*p.Cursor)))
	}
	if p.Seed {
		out = append(out, workflowFact("seed", styleWarn.Render(
			"this trigger has no cursor: a real poll now would record these as seeded and fire nothing")))
	}
	for _, j := range p.Events {
		out = append(out, "", "  "+styleTitle.Render(firstNonEmpty(j.EventID, "(no id)")))
		out = append(out, judgementLines(j)[1:]...)
	}
	return out
}

// judgementLines spells out what the pipeline decided about one event.
func judgementLines(j apiclient.TriggerJudgement) []string {
	out := []string{workflowFact("event", firstNonEmpty(j.EventID, "(no id)"))}
	if j.Matched {
		out = append(out, workflowFact("match", styleOK.Render("✓ passed")))
	} else {
		out = append(out, workflowFact("match", styleDim.Render("✗ missed "+j.MatchMiss)))
	}
	switch {
	case j.If == nil:
		out = append(out, workflowFact("if", styleDim.Render("— none, or not reached")))
	case *j.If:
		out = append(out, workflowFact("if", styleOK.Render("true")))
	default:
		out = append(out, workflowFact("if", styleDim.Render("false (rendered "+strconv.Quote(j.IfRendered)+")")))
	}
	if j.DedupeKey != "" {
		key := j.DedupeKey
		if j.WouldDedupe {
			key += styleWarn.Render("   already delivered — it would be deduped")
		}
		out = append(out, workflowFact("dedupe key", key))
	}
	if a := j.Action; a != nil {
		line := a.Type + " · " + a.Method + " " + a.Path
		if a.Branch != "" {
			line += " · branch " + a.Branch
		}
		if a.TaskID != nil {
			line += " · task #" + strconv.FormatInt(*a.TaskID, 10)
		}
		out = append(out, workflowFact("action", line))
		if len(a.Body) > 0 {
			out = append(out, workflowFact("body", styleDim.Render(string(a.Body))))
		}
	}
	out = append(out, workflowFact("outcome", trigOutcomeStyle(j.Outcome).Render(j.Outcome)))
	if j.Error != "" {
		out = append(out, workflowFact("error", styleBad.Render(j.Error)))
	}
	return out
}
