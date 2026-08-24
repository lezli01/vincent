package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/daemon"
)

// logPaneMinHeight keeps the pane usable on a short terminal: the three
// fixed blocks above it are short and constant, and the log is the part
// worth scrolling, so it takes whatever is left but never less than this.
const logPaneMinHeight = 3

func (d *daemonView) render(width, height int) string {
	if width > 0 {
		d.width = width
	}
	if height > 0 {
		d.height = height
	}
	var out []string
	out = append(out, d.identityLines()...)
	out = append(out, "")
	out = append(out, d.configLines()...)
	out = append(out, "")
	out = append(out, d.adapterLines()...)
	out = append(out, "")

	head := strings.Join(out, "\n")
	paneHeight := max(d.height-len(out)-1, logPaneMinHeight)
	return head + "\n" + d.renderLog(paneHeight)
}

// identityLines is what the daemon is: version, process, and the three paths
// someone reading this view is usually about to cd into.
func (d *daemonView) identityLines() []string {
	out := []string{" " + styleTitle.Render("daemon")}
	if !d.infoOK {
		return append(out, "  "+d.unavailable(d.infoErr, "identity"))
	}
	i := d.info
	version := i.Version
	if i.Commit != "" {
		version += styleDim.Render(" (" + shortCommit(i.Commit) + ")")
	}
	if i.Built != "" {
		version += styleDim.Render(" built " + i.Built)
	}
	out = append(out,
		field("version", version),
		field("uptime", formatUptime(i.Uptime(d.now())))+
			styleDim.Render("   pid "+strconv.Itoa(i.PID)),
		field("listening", i.Listen),
		field("data dir", d.dataDirOrUnknown()),
		field("log", d.logPathOrUnknown()),
	)
	if line, ok := orphanLine(i.Orphans); ok {
		out = append(out, line)
	}
	if line, ok := d.staleLine(d.infoErr, d.infoAt); ok {
		out = append(out, line)
	}
	return out
}

// orphanLine reports directories under the data dir that no task claims
// (task 005, §10) and names the command that clears them.
//
// It reports and offers no action, like the rest of this view (§15 view 6):
// running gc from here would be the daemon-stop button this view already
// refuses to have. A zero count prints nothing at all — a permanent "orphans:
// 0" is noise on every healthy daemon, and the line only earns its space when
// there is something to do about it.
func orphanLine(n int) (string, bool) {
	if n <= 0 {
		return "", false
	}
	noun := "directories"
	if n == 1 {
		noun = "directory"
	}
	return field("orphans", styleWarn.Render(
		fmt.Sprintf("%d %s no task claims", n, noun))+
		styleDim.Render("   reclaim with `vincent gc`")), true
}

// configLines is the configuration the daemon actually has loaded, which is
// not always the file on disk — it hot-reloads, and nothing announces it.
func (d *daemonView) configLines() []string {
	out := []string{" " + styleTitle.Render("config in effect")}
	if !d.configOK {
		return append(out, "  "+d.unavailable(d.configErr, "config"))
	}
	c := d.config
	out = append(out,
		field("max parallel tasks", strconv.Itoa(c.MaxParallelTasks)),
		field("agent timeout", c.Defaults.AgentTimeout)+
			styleDim.Render("   command "+c.Defaults.CommandTimeout+
				"   input "+c.Defaults.InputTimeout),
		field("transcript retention", strconv.Itoa(c.TranscriptRetentionDays)+" days")+
			styleDim.Render("   cap "+humanBytes(c.TranscriptMaxBytes)+" per run"),
		// Both halves of the §10 pair on one line: the remote one is inert
		// while the local one is off, so showing either alone would describe a
		// policy that cannot run.
		field("delete empty branch", onOff(c.DeleteEmptyBranchOnArchive))+
			styleDim.Render("   remote "+onOff(c.DeleteRemoteBranchOnArchive)),
		field("usage limit recheck", c.UsageLimitRecheck),
		field("log level", c.LogLevel),
		// What the file says, which is not necessarily what the board is
		// showing: `g` regroups for the session without writing anything.
		field("task grouping", groupSummary(c.TUI.Board.GroupBy)),
	)
	for _, name := range sortedKeys(c.Agents) {
		path := c.Agents[name].Path
		if path == "" {
			path = styleDim.Render("(resolved from PATH)")
		}
		out = append(out, field(name+" path", path))
	}
	if line, ok := d.staleLine(d.configErr, d.configAt); ok {
		out = append(out, line)
	}
	return out
}

// adapterLines is what the daemon can actually run right now, which is a
// different question from what the config names.
func (d *daemonView) adapterLines() []string {
	out := []string{" " + styleTitle.Render("adapters")}
	if !d.infoOK {
		return append(out, "  "+d.unavailable(d.infoErr, "adapters"))
	}
	if len(d.info.Agents) == 0 {
		return append(out, styleDim.Render("   no adapters configured"))
	}
	now := d.now()
	for _, a := range d.info.Agents {
		mark := styleOK.Render("✓")
		switch {
		case !a.Available:
			mark = styleBad.Render("✗")
		case a.NotAuthenticated():
			// A tick beside "not logged in" would contradict itself: the
			// binary is there, the adapter still cannot run a step.
			mark = styleWarn.Render("⚠")
		case a.QuotaSpent(now):
			// Same reasoning, temporary cause (task 026): the step would
			// start and stop again on the same wall. The board header uses
			// this glyph for it too, so one adapter reads the same way in
			// both views.
			mark = styleWarn.Render(quotaMark)
		}
		row := "   " + mark + " " + a.Name
		switch {
		case !a.Available && a.Error != "":
			row += "  " + styleBad.Render(a.Error)
		case !a.Available:
			row += "  " + styleBad.Render("not found")
		default:
			// The blocking condition leads: rows carry absolute binary paths
			// and elide to the pane width, so anything after the path is the
			// first thing lost on a narrow terminal. "not logged in" means
			// every step will fail and must not be what gets cut.
			if a.NotAuthenticated() {
				row += "  " + styleBad.Render("not logged in")
			}
			if a.QuotaSpent(now) {
				row += "  " + styleWarn.Render("usage limit "+quotaReset(a.Quota))
			}
			if a.Version != "" {
				row += "  " + styleDim.Render(a.Version)
			}
			if a.Path != "" {
				row += "  " + styleDim.Render(a.Path)
			}
			if !a.SupportsInput {
				row += "  " + styleWarn.Render("no interactive input")
			}
			row += "  " + styleDim.Render(quotaNote(a.Quota, now))
		}
		out = append(out, row)
	}
	return out
}

// quotaNote is this view's trailing statement of what vincent knows about an
// adapter's usage window (task 026). It is the one surface that says
// "unknown" out loud: the daemon view exists to list every fact about an
// adapter, and "nothing has been observed" is the honest answer for all three
// CLIs, none of which can report remaining quota without a real run (§9.2,
// §9.3, §9.7). The board header, which has no room to explain, says nothing
// instead.
//
// It trails the row deliberately — it is context, not a blocking condition, so
// it is the right thing to lose first on a narrow terminal. A window that is
// currently shut has already said so ahead of the version and path.
func quotaNote(q *apiclient.AgentQuota, now time.Time) string {
	switch {
	case q == nil:
		return "quota unknown"
	case q.SpentAt(now):
		return "quota " + q.Source
	default:
		return "quota ok · last spent " + q.ObservedAt.Local().Format("15:04")
	}
}

// onOff renders a boolean setting. "on"/"off" rather than "true"/"false":
// the view reports a policy in effect, not the YAML literal behind it.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// humanBytes renders a byte count the way the config file spells it, so the
// view and `config.yaml` agree on the wording.
func humanBytes(n int64) string {
	switch {
	case n <= 0:
		return "unlimited"
	case n%(1<<30) == 0:
		return strconv.FormatInt(n/(1<<30), 10) + "GB"
	case n%(1<<20) == 0:
		return strconv.FormatInt(n/(1<<20), 10) + "MB"
	case n%(1<<10) == 0:
		return strconv.FormatInt(n/(1<<10), 10) + "KB"
	}
	return strconv.FormatInt(n, 10) + "B"
}

func (d *daemonView) renderLog(height int) string {
	header := " " + styleTitle.Render("recent daemon log")
	if body, ok := d.logEmptyState(); ok {
		return header + "\n" + styleDim.Render("   "+body)
	}
	d.vp.SetWidth(max(d.width, 1))
	d.vp.SetHeight(max(height-1, 1))
	if d.logDirty {
		d.vp.SetContent(strings.Join(d.logLines, "\n"))
		d.logDirty = false
		if d.following {
			d.vp.GotoBottom()
		}
	}
	return header + "\n" + d.vp.View()
}

// logEmptyState separates the three reasons the pane can have no lines. A
// log that cannot be read, a log with nothing in it, and a read that has not
// happened yet are different problems, and only one of them is a problem.
func (d *daemonView) logEmptyState() (string, bool) {
	switch {
	case d.logErr != nil:
		return "no daemon log: " + errString(d.logErr), true
	case !d.logOK:
		return "reading the log…", true
	case len(d.logLines) == 0:
		return "the daemon log is empty", true
	}
	return "", false
}

// unavailable is what a daemon-supplied block says with nothing to show. It
// distinguishes a daemon that is not there from one that is there and failed
// to answer: the first is expected on this view, the second is not.
func (d *daemonView) unavailable(err error, what string) string {
	if !d.connected {
		return styleDim.Render("unavailable — the daemon is not reachable")
	}
	if err != nil {
		return styleBad.Render("could not read the " + what + ": " + errString(err))
	}
	return styleDim.Render("loading…")
}

// staleLine marks last-good content. A block that failed to refresh keeps
// what it had — the figures were true about a process that was running —
// but it must not present them as current.
func (d *daemonView) staleLine(err error, at time.Time) (string, bool) {
	switch {
	case !d.connected:
		return styleWarn.Render("   ⚠ stale · the daemon is not reachable" +
			asOf(at)), true
	case err != nil:
		return styleBad.Render("   ⚠ refresh failed: " + errString(err) + asOf(at)), true
	}
	return "", false
}

func asOf(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return " — showing " + at.Local().Format("15:04:05")
}

func (d *daemonView) dataDirOrUnknown() string {
	if d.dataDir == "" {
		return styleDim.Render("(unresolved)")
	}
	return d.dataDir
}

func (d *daemonView) logPathOrUnknown() string {
	if d.dataDir == "" {
		return styleDim.Render("(unresolved)")
	}
	return daemon.LogPath(d.dataDir)
}

func field(label, value string) string {
	return "   " + styleDim.Render(padRight(label, 22)) + value
}

// formatUptime drops to whole minutes past a minute: a figure that is
// re-rendered every couple of seconds should not appear to skip seconds.
func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
