package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// cursorStatus is cursor as the daemon reports it: available, observation-only,
// and carrying whatever the engine last watched close (§9.7).
func cursorStatus(q *apiclient.AgentQuota) apiclient.AgentStatus {
	return apiclient.AgentStatus{
		Name: "cursor", Available: true, SupportsInput: true,
		Version: "2026.01.02", Path: "/usr/local/bin/cursor-agent", Quota: q,
	}
}

// cursor has no usage surface to ask and reports nothing (§9.7), so task 082
// must not change one byte of its row. This is the regression the change most
// plausibly breaks, and the reason it is pinned rather than described.
func TestCursorAdapterRowIsUnchangedByReportedQuota(t *testing.T) {
	observed := testNow.Add(-3 * time.Hour)
	for _, tc := range []struct {
		name  string
		quota *apiclient.AgentQuota
		want  string
	}{
		{"nothing observed", nil, "quota unknown"},
		{"window still shut", &apiclient.AgentQuota{
			Spent: true, Source: apiclient.QuotaSourceObserved,
			ObservedAt: observed, ResetsAt: testNow.Add(time.Hour), ResetsAtReported: true,
		}, "quota observed"},
		{"recovered", &apiclient.AgentQuota{
			Source: apiclient.QuotaSourceObserved, ObservedAt: observed,
			ResetsAt: testNow.Add(-time.Hour), ResetsAtReported: true,
		}, "quota ok · last spent " + observed.Local().Format("15:04")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaNote(tc.quota, testNow); got != tc.want {
				t.Errorf("quotaNote = %q, want %q", got, tc.want)
			}
			d := newTestDaemonView(nil, nil)
			d.update(daemonInfoMsg{info: apiclient.Info{
				Agents: []apiclient.AgentStatus{cursorStatus(tc.quota)},
			}})
			lines := d.adapterLines()
			if len(lines) != 2 {
				t.Fatalf("adapterLines = %d lines, want the title and one row: %q", len(lines), lines)
			}
			want := "   " + styleOK.Render("✓") + " cursor"
			if tc.quota.SpentAt(testNow) {
				// A shut window takes the badge off the tick and says so
				// ahead of the version and path, exactly as before.
				want = "   " + styleWarn.Render(quotaMark) + " cursor" +
					"  " + styleWarn.Render("usage limit "+quotaReset(tc.quota))
			}
			want += "  " + styleDim.Render("2026.01.02") +
				"  " + styleDim.Render("/usr/local/bin/cursor-agent") +
				"  " + styleDim.Render(tc.want)
			if lines[1] != want {
				t.Errorf("cursor's row changed:\n got %q\nwant %q", lines[1], want)
			}
		})
	}
}

// A reading names its own windows, and the daemon view is the surface with
// room to spell them out (task 082).
func TestQuotaNoteRendersReportedWindows(t *testing.T) {
	read := testNow.Add(-90 * time.Second)
	stated := testNow.Add(2 * time.Hour)
	q := &apiclient.AgentQuota{
		Source: apiclient.QuotaSourceCodexAppServer, ObservedAt: read,
		Windows: []apiclient.AgentQuotaWindow{
			{
				Name: "primary", UsedPercent: 28.47, Window: "5h",
				ResetsAt: stated, ResetsAtReported: true,
			},
			{Name: "secondary", UsedPercent: 61, Window: "7d"},
		},
	}
	want := "quota codex app-server" +
		" · 5h 28.5% → " + stated.Local().Format("15:04") +
		" · 7d 61%" +
		" · read " + read.Local().Format("15:04")
	if got := quotaNote(q, testNow); got != want {
		t.Errorf("quotaNote =\n %q\nwant\n %q", got, want)
	}
}

// The distinction the arrow carries is the whole point of keeping it: `→` is a
// time the source stated, `≈` is one vincent worked out from
// usage_limit_recheck_interval, and a window that named none shows no time at
// all rather than a clock nobody is waiting for.
func TestQuotaWindowResetSeparatesStatedFromComputed(t *testing.T) {
	at := testNow.Add(time.Hour)
	stamp := at.Local().Format("15:04")
	for _, tc := range []struct {
		name   string
		window apiclient.AgentQuotaWindow
		want   string
	}{
		{"stated", apiclient.AgentQuotaWindow{ResetsAt: at, ResetsAtReported: true}, "→ " + stamp},
		{"computed", apiclient.AgentQuotaWindow{ResetsAt: at}, "≈ " + stamp},
		{"none named", apiclient.AgentQuotaWindow{}, ""},
	} {
		if got := quotaWindowReset(tc.window); got != tc.want {
			t.Errorf("%s: quotaWindowReset = %q, want %q", tc.name, got, tc.want)
		}
	}
	// The same rule on the block's scalars, which the row above the note and
	// the board badge both read.
	if got := quotaReset(&apiclient.AgentQuota{ResetsAt: at, ResetsAtReported: true}); got != "→ "+stamp {
		t.Errorf("quotaReset on a stated time = %q", got)
	}
	if got := quotaReset(&apiclient.AgentQuota{}); got != "" {
		t.Errorf("quotaReset on a reading that named no reset = %q, want nothing", got)
	}
}

// A window with no human label falls back to the wire name: a percentage
// attached to nothing is not a reading anybody can act on.
func TestQuotaWindowLabelFallsBackToTheWireName(t *testing.T) {
	note := quotaNote(&apiclient.AgentQuota{
		Source: apiclient.QuotaSourceClaudeStatusLine,
		Windows: []apiclient.AgentQuotaWindow{
			{Name: "five_hour", UsedPercent: 12.5},
		},
	}, testNow)
	if !strings.Contains(note, "claude status line · five_hour 12.5%") {
		t.Errorf("quotaNote = %q", note)
	}
}

// A reported window that is spent and named no reset must not produce a badge
// claiming the window reopens at midnight.
func TestQuotaBadgeOmitsAResetNobodyStated(t *testing.T) {
	spent := 100.0
	q := &apiclient.AgentQuota{
		Source: apiclient.QuotaSourceClaudeStatusLine, UsedPercent: &spent,
		Windows: []apiclient.AgentQuotaWindow{{Name: "five_hour", UsedPercent: 100, Window: "5h"}},
	}
	if !q.SpentAt(testNow) {
		t.Fatal("a reading at 100% is not spent")
	}
	if got := quotaBadge(q, testNow); got != quotaMark {
		t.Errorf("quotaBadge = %q, want the glyph alone", got)
	}
}
