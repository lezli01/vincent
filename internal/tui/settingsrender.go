package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/keymap"
)

// settingsLabelW is the label column of a setting's row.
const settingsLabelW = 30

// settingsNavW is the category list's outer width, and settingsHelpW the widest
// the explanation pane is drawn.
const (
	settingsNavW  = 40
	settingsHelpW = 48
	// settingsNavFoot is the rows under the category list.
	settingsNavFoot = 6
)

func (v *settingsView) render(width, height int) string {
	if width < 4 || height < 3 {
		return ""
	}
	if !v.frameless() || width < mgmtWideMinW {
		return v.renderNarrow(width, height)
	}
	search := v.searchBox(width)
	v.searchRows = len(search)
	h := max(height-len(search), 3)
	helpW := min(width*25/100, settingsHelpW)
	midW := width - settingsNavW - helpW
	out := joinColumns(v.navPane(settingsNavW, h), joinColumns(v.settingsPane(midW, h), v.helpPane(helpW, h)))
	return strings.Join(append(search, out...), "\n")
}

// renderNarrow is the settings alone, under the category they belong to. A
// terminal too small for the panes keeps every setting and every key.
func (v *settingsView) renderNarrow(width, height int) string {
	category := settingsCatalog[v.category]
	head := styleTitle.Render(category.glyph+" "+category.name) +
		styleDim.Render("  "+strconv.Itoa(v.category+1)+" of "+strconv.Itoa(len(settingsCatalog))+"  tab next")
	lines := []string{ansi.Truncate(head, width, "…")}
	if v.searching || v.search.Value() != "" {
		lines = []string{toneBlue.Render(ic("⌕ ")) + v.searchText(width-2)}
	}
	s := v.settingsStack(width)
	return strings.Join(append(lines, s.view(max(height-len(lines), 1))...), "\n")
}

func (v *settingsView) searchText(width int) string {
	if !v.searching && v.search.Value() == "" {
		return styleDim.Render(v.search.Placeholder())
	}
	v.search.SetWidth(max(width, 10))
	return strings.Join(v.search.rows(), " ")
}

// searchBox is the box above the panes.
func (v *settingsView) searchBox(width int) []string {
	edges, border := edgesRounded, styleDim
	if v.searching {
		edges, border = edgesThick, styleFocus
	}
	return drawBox("", "", []string{toneBlue.Render(ic("⌕ ")) + v.searchText(width-8)}, width, 3, edges, border)
}

// navPane lists the categories, with how many settings differ from their default
// or, during a search, how many match.
func (v *settingsView) navPane(w, h int) []string {
	inner := max(w-4, 1)
	matches := map[int]int{}
	query := strings.TrimSpace(v.search.Value())
	selected := v.category
	if query != "" {
		rows := v.rows()
		for _, r := range rows {
			matches[r.category]++
		}
		if row, ok := v.current(); ok {
			selected = row.category
		}
	}
	var body []string
	v.navHits = v.navHits[:0]
	total := 0
	// The foot is six rows at most. A short terminal closes the gaps between the
	// categories, so all of them stay on screen.
	roomy := h-2-settingsNavFoot >= 2*len(settingsCatalog)+1
	for i, c := range settingsCatalog {
		changed := v.changedIn(i)
		total += changed
		right := styleDim.Render(strconv.Itoa(settingsCount(c)))
		if changed > 0 {
			right = styleWarn.Render(ic("●")+" "+strconv.Itoa(changed)) + "  " + right
		}
		if query != "" {
			right = styleDim.Render("–")
			if matches[i] > 0 {
				right = toneBlue.Render(strconv.Itoa(matches[i]))
			}
		}
		if i == selected {
			line := padBetweenCells(styleFocus.Bold(true).Render(ic(c.glyph)+" "+c.name), right, inner-4)
			v.navHits = append(v.navHits, settingsNavHit{y0: v.searchRows + 1 + len(body), y1: v.searchRows + 1 + len(body) + 3, category: i})
			body = append(body, drawBox("", "", []string{line}, inner, 3, edgesThick, styleFocus)...)
			continue
		}
		v.navHits = append(v.navHits, settingsNavHit{y0: v.searchRows + 1 + len(body), y1: v.searchRows + 2 + len(body), category: i})
		name := c.name
		if query != "" && matches[i] == 0 {
			name = styleDim.Render(name)
		}
		body = append(body, "  "+padBetweenCells(toneMagenta.Render(ic(c.glyph))+" "+name, right, inner-4))
		if roomy {
			body = append(body, "")
		}
	}
	foot := []string{
		styleDim.Render(strings.Repeat("─", inner)),
		styleWarn.Render(ic("●")+" "+strconv.Itoa(total)) + styleDim.Render(" differ from the default"),
		styleOK.Render(ic("✓ ")) + styleDim.Render("changes apply at once"),
		"",
	}
	if v.infoOK && v.info.ConfigPath != "" {
		path := v.info.ConfigPath
		foot = append(foot, styleDim.Render(ansi.TruncateLeft(path, max(ansi.StringWidth(path)-inner, 0), "…")))
	}
	foot = append(foot, styleKey.Render(opKey(keymap.Editor))+styleDim.Render(" open in $EDITOR"))
	for len(body) < h-2-len(foot) {
		body = append(body, "")
	}
	body = append(body[:min(len(body), max(h-2-len(foot), 0))], foot...)
	return drawBox(toneBlue.Bold(true).Render(ic("⚙ Settings")), toneBlue.Render(strconv.Itoa(len(v.keys))), body, w, h, edgesRounded, styleDim)
}

func settingsCount(c settingsCategory) int {
	n := 0
	for _, s := range c.sections {
		n += len(s.items)
	}
	return n
}

// settingsPane is the settings of the selected category, or the search's results.
func (v *settingsView) settingsPane(w, h int) []string {
	category := settingsCatalog[v.category]
	title := ic(category.glyph) + " " + category.name
	if strings.TrimSpace(v.search.Value()) != "" {
		title = ic("⌕ Results")
	}
	right := ""
	switch {
	case v.form != nil && v.form.confirming:
		right = formPill(false, attentionBadge+" confirm")
	case v.note != "" && v.noteBad:
		right = styleBad.Render(ansi.Truncate("⚠ "+oneLine(v.note), max(w/2, 10), "…"))
	case v.note != "":
		// Room beside the title: a saved note says what the value was and
		// how to put it back (#1057), and must not run over the corner.
		right = styleOK.Render(ansi.Truncate(ic("✓ ")+oneLine(v.note), max(w-ansi.StringWidth(title)-8, 10), "…"))
	}
	s := v.settingsStack(max(w-4, 1))
	// Where each setting's lines landed, for a click (#1065): the pane sits
	// right of the category list, under the search box, inside its border.
	shown := s.view(max(h-2, 1))
	start := s.viewStart(max(h-2, 1))
	v.paneHits = v.paneHits[:0]
	for k, line := range shown {
		if i := start + k; i < len(s.tags) && s.tags[i] != "" {
			v.paneHits = append(v.paneHits, settingsHit{
				y: v.searchRows + 1 + k, x0: settingsNavW + 2, path: s.tags[i], text: ansi.Strip(line),
			})
		}
	}
	return drawBox(styleTitle.Render(title), right, shown, w, h, edgesRounded, styleDim)
}

// settingsStack draws the visible settings as titled sections, the one holding
// the cursor thick.
func (v *settingsView) settingsStack(inner int) formStack {
	var s formStack
	rows := v.rows()
	if len(rows) == 0 {
		text := "Loading…"
		if v.search.Value() != "" {
			text = "No setting matches that search."
		}
		s.add([]string{styleDim.Render(text)}, false)
		return s
	}
	if query := strings.TrimSpace(v.search.Value()); query != "" {
		s.add([]string{styleDim.Render(plural(len(rows), "setting matches", "settings match") + " “" + query + "”"), ""}, false)
	}
	for start := 0; start < len(rows); {
		end := start
		for end < len(rows) && rows[end].category == rows[start].category && rows[end].section == rows[start].section {
			end++
		}
		section := settingsCatalog[rows[start].category].sections[rows[start].section]
		right := ""
		if section.note != "" {
			right = styleDim.Render(section.note)
		}
		var body, tags []string
		focused := false
		for i, row := range rows[start:end] {
			if i > 0 {
				body = append(body, "")
				tags = append(tags, "")
			}
			here := row.item.path == v.selected
			focused = focused || here
			lines := v.settingLines(row, here, max(inner-4, 1))
			body = append(body, lines...)
			for range lines {
				tags = append(tags, row.item.path)
			}
		}
		// The box's top and bottom edges carry no setting.
		s.addTagged(formSection(ic(section.title), right, body, inner, focused), append(append([]string{""}, tags...), ""), focused)
		if focused && v.form != nil && v.form.confirming {
			s.extend(v.confirmCard(inner))
		}
		start = end
	}
	return s
}

// settingLines is one setting: its label and control, what it does, and whether
// it differs from the default.
func (v *settingsView) settingLines(row settingsRow, here bool, w int) []string {
	if here && v.form != nil {
		return v.editLines(row, w)
	}
	name := row.item.label
	if here {
		name = styleFocus.Bold(true).Render(name)
	}
	left := formRowMark(here) + padRightCells(name, settingsLabelW) + v.control(row)
	right := ""
	if def := row.key.def(); def != v.value(row.key) {
		right = styleWarn.Render(ic("●")+" ") + styleDim.Render("default "+settingsDefault(row.key))
	}
	return []string{padBetweenCells(ansi.Truncate(left, w-ansi.StringWidth(right)-1, "…"), right, w), "  " + styleDim.Render(row.item.desc)}
}

// padRightCells pads styled text to width cells.
func padRightCells(s string, width int) string {
	return s + strings.Repeat(" ", max(width-ansi.StringWidth(s), 1))
}

// settingsValue spells a value for reading: nothing set says so.
func settingsValue(s string) string {
	if strings.TrimSpace(s) == "" {
		return "empty"
	}
	return s
}

// settingsDefault spells a key's default the way its control shows values.
func settingsDefault(k configKey) string {
	if k.kind == kindBool {
		if k.def() == "true" {
			return "on"
		}
		return "off"
	}
	return settingsValue(k.def())
}

// control draws a setting's value in the form that changes it.
func (v *settingsView) control(row settingsRow) string {
	value := v.value(row.key)
	switch {
	case row.key.kind == kindBool:
		return formChip("on", value == "true") + " " + formChip("off", value != "true")
	case row.key.kind == kindEnum:
		chips := make([]string, 0, len(row.key.choices))
		for _, c := range row.key.choices {
			chips = append(chips, formChip(c, c == value))
		}
		return strings.Join(chips, " ")
	case row.key.kind == kindInt:
		return styleDim.Render("− ") + styleTitle.Render(value) + styleDim.Render(" +") + v.liveNote(row.item.path)
	case len(row.item.presets) > 0:
		return presetChips(row.item.presets, value)
	case strings.HasPrefix(row.item.path, "agents."):
		return v.agentControl(row, value)
	case value == "":
		return styleDim.Render("not set")
	}
	return oneLine(row.key.display(v.config))
}

// presetChips draws a duration's usual values, and the one in force when it is
// none of them.
func presetChips(presets []string, value string) string {
	chips := make([]string, 0, len(presets)+1)
	matched := false
	for _, p := range presets {
		on := sameDuration(p, value)
		matched = matched || on
		label := p
		if sameDuration(p, "0s") {
			label = "off"
		}
		chips = append(chips, formChip(label, on))
	}
	if !matched {
		chips = append(chips, formChip(value, true))
	}
	return strings.Join(chips, " ")
}

// liveNote says how much of a limit is in use right now.
func (v *settingsView) liveNote(path string) string {
	if path != "max_parallel_tasks" || !v.infoOK {
		return ""
	}
	return styleDim.Render("   " + strconv.Itoa(v.info.Slots.Used) + " in use")
}

// agentControl is an agent's program and what the daemon found there.
func (v *settingsView) agentControl(row settingsRow, value string) string {
	out := styleDim.Render("from PATH")
	if value != "" {
		out = oneLine(value)
	}
	if !v.infoOK {
		return out
	}
	for _, a := range v.info.Agents {
		if a.Name != row.item.label {
			continue
		}
		if !a.Available {
			return out + "   " + styleWarn.Render("⚠ not found")
		}
		return out + "   " + styleOK.Render(ic("✓ ")+oneLine(a.Version))
	}
	return out
}

// editLines is the setting with its editor open.
func (v *settingsView) editLines(row settingsRow, w int) []string {
	f := v.form
	out := []string{formRowMark(true) + styleFocus.Bold(true).Render(row.item.label) + "   " + styleDim.Render(row.item.desc)}
	if f.key.kind == kindBool || f.key.kind == kindEnum {
		chips := make([]string, 0, len(f.key.choices))
		for i, c := range f.key.choices {
			chips = append(chips, formChip(c, i == f.choice))
		}
		out = append(out, "  "+strings.Join(chips, " "))
	} else {
		f.input.SetWidth(max(w-2, 10))
		out = append(out, fieldRows("  ", f.input)...)
		if len(f.key.choices) > 0 {
			out = append(out, styled(wrapText("accepted: "+strings.Join(f.key.choices, " "), max(w-2, 10), wrapOpts{prefix: "  ", indent: 2}), styleDim)...)
		}
	}
	if f.key.path == "branch_template" {
		out = append(out, v.templateLines()...)
	}
	if f.key.restart {
		out = append(out, "  "+styleWarn.Render("applies after the daemon restarts"))
	}
	if f.err != "" {
		out = append(out, styled(wrapText("⚠ "+f.err, max(w-2, 10), wrapOpts{prefix: "  ", indent: 2}), styleBad)...)
	}
	if f.saving {
		out = append(out, "  "+styleDim.Render("saving…"))
	}
	return out
}

// templateLines are the branch template's field chips and the daemon's preview
// of the name it makes.
func (v *settingsView) templateLines() []string {
	chips := make([]string, 0, len(branchTemplateFields))
	for i, field := range branchTemplateFields {
		chips = append(chips, formChip(field, i == v.fieldAt))
	}
	out := []string{"", "  " + styleDim.Render("insert   ") + strings.Join(chips, " "), ""}
	switch {
	case v.form.input.Value() == "":
		return append(out, "  "+styleDim.Render("becomes  the built-in name"))
	case v.previewErr != "":
		return append(out, "  "+styleBad.Render("⚠ "+oneLine(v.previewErr)))
	case v.previewFor != v.form.input.Value():
		return append(out, "  "+styleDim.Render("becomes  …"))
	case v.preview.Error != "":
		return append(out, "  "+styleBad.Render("⚠ "+oneLine(v.preview.Error)))
	}
	return append(out,
		"  "+styleDim.Render("becomes  ")+styleOK.Render(ic("✓ "))+toneCyan.Render(v.preview.Branch),
		"  "+styleDim.Render("         for task #"+strconv.FormatInt(v.preview.TaskID, 10)+", “"+v.preview.Title+"”"))
}

// confirmCard asks before a key that decides what the daemon executes or exposes
// applies, with both values whole so a long one cannot hide what is at stake.
func (v *settingsView) confirmCard(inner int) []string {
	f := v.form
	text := max(inner-4, 10)
	body := styled(wrapText(settingsConsequence(f.key.path), text, wrapOpts{}), styleTitle)
	body = append(body, "")
	body = append(body, styled(wrapText("from  "+settingsValue(f.key.read(v.config)), text, wrapOpts{indent: 6}), styleDim)...)
	body = append(body, styled(wrapText("to    "+f.displayValue(), text, wrapOpts{indent: 6}), styleWarn)...)
	body = append(body, "", styleKey.Render("y")+styleDim.Render(" apply this change    ")+styleKey.Render("esc")+styleDim.Render(" go back"))
	return drawBox(styleWarn.Bold(true).Render(attentionBadge+" Change what vincent runs or exposes?"), "", body, inner, len(body)+2, edgesThick, styleWarn)
}

// settingsConsequence says what confirming a dangerous key would do.
func settingsConsequence(path string) string {
	switch {
	case strings.HasPrefix(path, "agents."):
		return "vincent will start this program for every step and chat of this agent, as you."
	case path == "notify.command":
		return "vincent will run this command as you whenever a task reaches a chosen state."
	case strings.HasPrefix(path, "environment."):
		return "This changes the variables every agent and command receives."
	case path == "listen":
		return "The daemon will listen on this address after its next restart."
	case path == "triggers.enabled":
		return "Trigger files will be able to start agents as you, without you asking."
	case path == "backup.dir":
		return "Backups hold config.yaml and every transcript; anyone who can read this folder can read them."
	}
	return "This changes what the daemon executes or exposes."
}

// helpPane explains the selected setting.
func (v *settingsView) helpPane(w, h int) []string {
	inner := max(w-4, 1)
	title := styleTitle.Render("About this setting")
	row, ok := v.current()
	if !ok {
		return drawBox(title, "", nil, w, h, edgesRounded, styleDim)
	}
	fact := func(k, val string) string { return padBetweenCells(styleDim.Render(k), oneLine(val), inner) }
	body := []string{styleTitle.Render(row.item.label), toneCyan.Render(ansi.Truncate(row.key.path, inner, "…")), ""}
	body = append(body, wrapText(row.item.desc+".", inner, wrapOpts{})...)
	if row.key.help != "" {
		body = append(body, "")
		body = append(body, styled(wrapText(row.key.help, inner, wrapOpts{}), styleDim)...)
	}
	if row.key.dangerous {
		body = append(body, "", styleWarn.Bold(true).Render(attentionBadge+" Asks before saving"))
		body = append(body, styled(wrapText("This decides what runs on your machine or who can read your data.", inner, wrapOpts{}), styleDim)...)
	}
	if row.item.path == "max_parallel_tasks" && v.infoOK {
		limit := v.config.MaxParallelTasks
		body = append(body, "", styleTitle.Render("Right now"), fact("running", strconv.Itoa(v.info.Slots.Used)+" of "+strconv.Itoa(limit)),
			fact("agents at most", strconv.Itoa(limit+v.config.MaxParallelChats)))
	}
	body = append(body, "", styleTitle.Render("This setting"), fact("default", settingsDefault(row.key)), fact("applies", settingsApplies(row.key)))
	body = append(body, "", styleKey.Render("⌫")+styleDim.Render(" reset to the default"))
	return drawBox(title, "", body, w, h, edgesRounded, styleDim)
}

// settingsApplies says when a change takes effect.
func settingsApplies(k configKey) string {
	switch {
	case k.restart:
		return "after a daemon restart"
	case k.path == "tui.default_project":
		return "the next time vincent opens"
	}
	return "immediately"
}

// footerSummary says how the screen stands, for the status bar.
func (v *settingsView) footerSummary(width int) string {
	if !v.frameless() || width < mgmtWideMinW {
		return ""
	}
	out := styleDim.Render(settingsCatalog[v.category].name)
	if q := strings.TrimSpace(v.search.Value()); q != "" {
		out = styleDim.Render(plural(len(v.rows()), "match", "matches"))
	}
	if v.form != nil && v.form.confirming {
		out += styleDim.Render("  ·  ") + styleWarn.Render(attentionBadge+" waiting for your answer")
	}
	return " " + out + "  "
}
