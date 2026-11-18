package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The daemon dashboard (task 150.7): the daemon and its work, the config and the
// database side by side, a card per adapter under them, and the log taking what
// is left.

const (
	// adapterCardH is one adapter card's outer height.
	adapterCardH = 8
	// adapterCardsMinH is the shortest screen that spends the rows on cards: a
	// shorter one keeps the adapters to a line each, so the log stays readable.
	adapterCardsMinH = 30
	// workMeterW is the widest the slots meter is drawn.
	workMeterW = 16
)

// renderWide draws the dashboard.
func (d *daemonView) renderWide(width, height int) string {
	identity, config, database := d.identityLines()[1:], d.configLines()[1:], d.databaseLines()[1:]
	if d.doctorOK {
		database = append(database, issueArchiveLines(d.doctor.IssueArchive, width/3)...)
	}
	work := d.workLines()
	adapters := d.adapterRows(width, height)
	stacked := len(identity) + len(work) + 4
	topH := min(max(stacked, len(config)+2, len(database)+2), max(height/2, 10))
	logH := max(height-topH-len(adapters), 5)

	w := width / 3
	workH := min(len(work)+2, max(topH-3, 2))
	configBorder := styleDim
	if d.focusConfig {
		configBorder = styleFocus
	}
	left := drawBox(styleTitle.Render(ic("◉ Daemon")), styleDim.Render(fmt.Sprintf("up %s · pid %d", formatUptime(d.info.Uptime(d.now())), d.info.PID)), identity, w, topH-workH, edgesRounded, styleDim)
	left = append(left, drawBox(styleTitle.Render(ic("▶ Work")), "", work, w, workH, edgesRounded, styleDim)...)
	top := joinColumns(left, joinColumns(
		drawBox(styleTitle.Render(ic("⚙ Config in effect")), "", config, w, topH, edgesRounded, configBorder),
		drawBox(styleTitle.Render(ic("▤ Database")), "", database, width-2*w, topH, edgesRounded, styleDim),
	))
	top = append(top, adapters...)
	log := strings.Split(d.renderLog(max(logH-1, 2)), "\n")
	if len(log) > 1 {
		log = log[1:]
	}
	return strings.Join(append(top, drawBox(styleTitle.Render(ic("≡ Recent log")), "", log, width, logH, edgesRounded, styleDim)...), "\n")
}

// workLines is what the daemon is doing with its concurrency slots.
func (d *daemonView) workLines() []string {
	var out []string
	if d.pauseOK {
		out = append(out, pauseStateLabel(d.pause)+styleDim.Render("   "+opKey(keymap.PauseAll)+" pause / continue all"))
	}
	if !d.infoOK {
		return append(out, d.unavailable(d.infoErr, "work"))
	}
	s := d.info.Slots
	out = append(out, workMeter(s.Used, d.info.MaxParallelTasks))
	return append(out, strings.Join([]string{
		toneCyan.Render(ic("▶ ") + strconv.Itoa(max(s.Used-s.AwaitingInput, 0)) + " working"),
		styleWarn.Render(attentionBadge + " " + strconv.Itoa(s.AwaitingInput) + " waiting on you"),
		styleDim.Render(ic("⑂ ") + strconv.Itoa(s.Lanes) + " lanes"),
	}, "   "))
}

// workMeter draws the slots in use. The figures say it too, so the bar is never
// the only way to read it.
func workMeter(used, capacity int) string {
	text := strconv.Itoa(used) + " of " + strconv.Itoa(capacity) + " slots in use"
	if capacity <= 0 {
		return styleTitle.Render(text)
	}
	cells := min(capacity, workMeterW)
	filled := min(used*cells/capacity, cells)
	if used > 0 {
		filled = max(filled, 1)
	}
	return toneBlue.Render(strings.Repeat("█", filled)) + styleDim.Render(strings.Repeat("░", cells-filled)) + "  " + styleTitle.Render(text)
}

// adapterRows is the adapters as cards, with the offers that follow them. A
// short screen, or a daemon with no adapters to show, keeps the plain lines.
func (d *daemonView) adapterRows(width, height int) []string {
	if !d.infoOK || len(d.info.Agents) == 0 || height < adapterCardsMinH {
		lines := d.adapterLines()[1:]
		return drawBox(styleTitle.Render(ic("◈ Adapters")), "", lines, width, min(len(lines)+2, max(height/4, 4)), edgesRounded, styleDim)
	}
	n := len(d.info.Agents)
	cardW := width / n
	row := make([]string, adapterCardH)
	for i, a := range d.info.Agents {
		w := cardW
		if i == n-1 {
			w = width - cardW*(n-1)
		}
		row = joinColumns(row, d.adapterCard(a, w))
	}
	for _, offer := range []func() (string, bool){d.statusLineLine, d.skillsLine} {
		if line, ok := offer(); ok {
			row = append(row, ansi.Truncate(line, width, "…"))
		}
	}
	return row
}

// adapterCard is one adapter. What stops it running leads, and colours the
// frame; the glyph and the words say the same without colour.
func (d *daemonView) adapterCard(a apiclient.AgentStatus, w int) []string {
	inner := max(w-4, 1)
	status, border := d.adapterStatus(a)
	body := []string{status}
	if a.Available {
		body = append(body, styleTitle.Render(valueOr(a.Version, "version unknown")))
		if limits := adapterLimits(a); limits != "" {
			body = append(body, limits)
		}
		note := quotaNote(a.Quota, d.now())
		body = append(body, styled(wrapText(note, inner, wrapOpts{maxLines: 2}), styleDim)...)
		if a.Path != "" {
			body = append(body, styleDim.Render(ansi.TruncateLeft(a.Path, max(ansi.StringWidth(a.Path)-inner, 0), "…")))
		}
	}
	return drawBox(toneMagenta.Bold(true).Render(ic("◈ ")+a.Name), "", body, w, adapterCardH, edgesRounded, border)
}

// adapterStatus is the card's first line and the colour of its frame.
func (d *daemonView) adapterStatus(a apiclient.AgentStatus) (string, lipgloss.Style) {
	now := d.now()
	switch {
	case !a.Available:
		return styleBad.Render(ic("✗ ") + valueOr(oneLine(a.Error), "not found")), styleBad
	case a.NotAuthenticated():
		return styleWarn.Render("⚠ not logged in"), styleWarn
	case a.QuotaSpent(now):
		return styleWarn.Render(quotaMark + " " + strings.TrimSpace("usage limit "+quotaReset(a.Quota))), styleWarn
	}
	return styleOK.Render(ic("✓ ready")), styleDim
}

// adapterLimits is what this adapter cannot do here.
func adapterLimits(a apiclient.AgentStatus) string {
	out := strings.TrimSpace(adapterVerdicts(a))
	if !a.SupportsInput {
		if out != "" {
			out += "  "
		}
		out += styleWarn.Render("no interactive input")
	}
	return out
}
