package tui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 157: per-turn usage in the heading, the context gauge on the
// conversation pane's edge, the totals line under 150 columns, and the
// compact key.

func f64(f float64) *float64 { return &f }

// usageTurn is a finished turn that reported everything claude reports.
func usageTurn(seq int, ctxTokens, window int64) apiclient.ChatTurn {
	ended := time.Date(2026, 8, 31, 11, 59, 0, 0, time.UTC)
	t := apiclient.ChatTurn{
		ID: int64(seq), ChatID: 1, Seq: seq, State: "done", Prompt: "ask", ResultText: "answer",
		StartedAt: ended.Add(-42 * time.Second), EndedAt: &ended, DurationMS: i64(42_000),
		InputTokens: 200, OutputTokens: 1100, CostUSD: f64(0.04),
		CacheReadTokens: i64(16_000), CacheCreationTokens: i64(2_000),
	}
	if ctxTokens > 0 {
		t.ContextTokens = i64(ctxTokens)
	}
	if window > 0 {
		t.ContextWindow = i64(window)
	}
	return t
}

func TestChatTurnHeadingUsage(t *testing.T) {
	full := usageTurn(3, 0, 0)
	copilot := full
	copilot.CostUSD = nil
	codex := full
	codex.CacheReadTokens, codex.CacheCreationTokens = nil, nil
	running := full
	running.State, running.EndedAt, running.DurationMS = "running", nil, nil
	silent := full
	silent.InputTokens, silent.OutputTokens, silent.CacheReadTokens, silent.CacheCreationTokens = 0, 0, nil, nil
	silent.CostUSD = nil

	for _, tc := range []struct {
		name    string
		turn    apiclient.ChatTurn
		want    []string
		notWant []string
	}{
		{"claude", full, []string{"turn 3", "42.0s", "18.2k↑ 1.1k↓", "$0.04"}, nil},
		{"copilot reports no dollars", copilot, []string{"42.0s", "18.2k↑ 1.1k↓"}, []string{"$"}},
		{"cache counted in the input", codex, []string{"200↑ 1.1k↓"}, nil},
		{"a running turn shows no usage", running, []string{"turn 3"}, []string{"s ·", "↑", "↓", "$"}},
		{"nothing reported but the clock", silent, []string{"42.0s"}, []string{"↑", "↓", "$"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ansi.Strip(chatTurnHeading("claude", tc.turn, "1m"))
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("heading %q lacks %q", got, w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("heading %q carries %q", got, w)
				}
			}
		})
	}
}

func TestChatTokensFormatter(t *testing.T) {
	for n, want := range map[int64]string{950: "950", 18_200: "18.2k", 122_000: "122.0k", 1_100_000: "1.1M", 999_999: "1.0M"} {
		if got := chatTokens(n); got != want {
			t.Errorf("chatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

// gaugeChat is an open chat whose turns carry the given context figures, in
// the workspace when wide enough.
func gaugeChat(t *testing.T, turns ...apiclient.ChatTurn) *chatView {
	t.Helper()
	side := chatWorkspaceFixture(t)
	v := chatViewFixture()
	v.client = offlineClient()
	v.side, v.now = side, side.now
	v.turns = turns
	return v
}

func renderChatAt(v *chatView, w int) string {
	v.update(tea.WindowSizeMsg{Width: w, Height: 30})
	return ansi.Strip(v.render(w, 30))
}

func TestChatGaugeAtEveryWidth(t *testing.T) {
	for _, w := range []int{80, 120, 200} {
		t.Run("with a window", func(t *testing.T) {
			out := renderChatAt(gaugeChat(t, usageTurn(1, 50_000, 200_000), usageTurn(2, 122_000, 200_000)), w)
			if !strings.Contains(out, "61%") {
				t.Errorf("at %d columns the gauge does not say 61%%:\n%s", w, out)
			}
			wantBar := w >= 100
			if hasBar := strings.Contains(out, "ctx ▰▰▰▱▱ 61%"); hasBar != wantBar {
				t.Errorf("at %d columns the bar drawn = %v, want %v:\n%s", w, hasBar, wantBar, out)
			}
			if !wantBar && !strings.Contains(out, "ctx 61%") {
				t.Errorf("at %d columns the gauge did not collapse to ctx 61%%:\n%s", w, out)
			}
		})
		t.Run("without a window", func(t *testing.T) {
			out := renderChatAt(gaugeChat(t, usageTurn(1, 122_000, 0)), w)
			if !strings.Contains(out, "ctx 122.0k") || strings.Contains(out, "%") {
				t.Errorf("at %d columns a window-less gauge is wrong:\n%s", w, out)
			}
		})
		t.Run("no data", func(t *testing.T) {
			out := renderChatAt(gaugeChat(t, usageTurn(1, 0, 0)), w)
			if strings.Contains(out, "ctx") {
				t.Errorf("at %d columns a gauge was drawn with no context figure:\n%s", w, out)
			}
		})
	}
}

// Every line of every width still fits: the gauge never widens the frame.
func TestChatGaugeFits(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 149, 150, 200} {
		v := gaugeChat(t, usageTurn(1, 190_000, 200_000))
		v.compactCmds = map[string]string{"claude": "/compact"}
		v.update(tea.WindowSizeMsg{Width: w, Height: 30})
		for i, l := range strings.Split(v.render(w, 30), "\n") {
			if got := ansi.StringWidth(l); got > w {
				t.Errorf("at %d columns line %d is %d wide", w, i, got)
			}
		}
	}
}

func TestChatGaugeWarnsPastEightyPercent(t *testing.T) {
	cool, hot := chatGauge([]apiclient.ChatTurn{usageTurn(1, 100_000, 200_000)}, true, "", "")
	if hot || strings.Contains(ansi.Strip(cool), "!") {
		t.Errorf("50%% warned: %q", ansi.Strip(cool))
	}
	gauge, hot := chatGauge([]apiclient.ChatTurn{usageTurn(1, 170_000, 200_000)}, true, "", "")
	if !hot {
		t.Fatal("85% did not warn")
	}
	if gauge != styleWarn.Bold(true).Render(ansi.Strip(gauge)) {
		t.Errorf("85%% is not yellow: %q", gauge)
	}
	// NO_COLOR: the colour goes, the `!` and the figures stay.
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ASCII}
	if _, err := w.Write([]byte(gauge)); err != nil {
		t.Fatal(err)
	}
	plain := buf.String()
	if strings.Contains(plain, "[33") || strings.Contains(plain, "[1;33") {
		t.Errorf("yellow survived NO_COLOR: %q", plain)
	}
	if got := ansi.Strip(plain); got != "! ctx ▰▰▰▰▱ 85%" {
		t.Errorf("monochrome gauge is %q", got)
	}
}

func TestChatTotalsLineOnlyBelowTheDetailsPane(t *testing.T) {
	for _, tc := range []struct {
		w    int
		want bool
	}{{80, true}, {120, true}, {149, true}, {150, false}, {200, false}} {
		out := renderChatAt(gaugeChat(t, usageTurn(1, 0, 0), usageTurn(2, 0, 0)), tc.w)
		if got := strings.Contains(out, "Σ 2.6k tokens  ·  $0.08"); got != tc.want {
			t.Errorf("at %d columns the totals line drawn = %v, want %v:\n%s", tc.w, got, tc.want, out)
		}
	}
	if line := chatTotalsLine(nil); line != "" {
		t.Errorf("an empty conversation has totals %q", line)
	}
}

// sendRecorder is a daemon that records every message a chat is sent.
func sendRecorder(t *testing.T) (*apiclient.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var sent []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/send") {
			var body struct {
				Message string `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			sent = append(sent, body.Message)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(apiclient.ChatTurn{ID: 9, ChatID: 1, Seq: 2, State: "running"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	return apiclient.New(ts.URL, "token"), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sent...)
	}
}

func TestChatCompactKey(t *testing.T) {
	t.Run("an agent without a compact command", func(t *testing.T) {
		client, sent := sendRecorder(t)
		v := gaugeChat(t, usageTurn(1, 190_000, 200_000))
		v.client = client
		v.chat.Agent = "codex"
		v.compactCmds = map[string]string{"claude": "/compact"}
		if g, _ := chatGauge(v.turns, true, v.compactCommand(), v.gaugeCompactKey()); strings.Contains(g, "compact") {
			t.Errorf("the gauge offers compaction to codex: %q", ansi.Strip(g))
		}
		for _, b := range v.liveBindings(bindingsFor(ctxChat)) {
			if b.key == "alt+k" {
				t.Error("the compact key is offered to codex")
			}
		}
		_, cmd := v.updateKey(registryKey(t, "alt+k"))
		if v.compacting || cmd != nil {
			t.Fatal("alt+k asked to compact codex")
		}
		if got := sent(); len(got) != 0 {
			t.Fatalf("sent %q", got)
		}
	})

	t.Run("claude: asks, then sends exactly /compact", func(t *testing.T) {
		client, sent := sendRecorder(t)
		v := gaugeChat(t, usageTurn(1, 190_000, 200_000))
		v.client = client
		v.compactCmds = map[string]string{"claude": "/compact"}
		v.composer.SetValue("a draft I am still writing")
		if g, _ := chatGauge(v.turns, true, v.compactCommand(), v.gaugeCompactKey()); !strings.Contains(ansi.Strip(g), "alt+k compact") {
			t.Errorf("the hot gauge does not name the key: %q", ansi.Strip(g))
		}
		offered := false
		for _, b := range v.liveBindings(bindingsFor(ctxChat)) {
			offered = offered || b.key == "alt+k"
		}
		if !offered {
			t.Error("the compact key is not offered on an idle claude chat")
		}
		if _, cmd := v.updateKey(registryKey(t, "alt+k")); cmd != nil || !v.compacting {
			t.Fatal("alt+k did not ask first")
		}
		if got := sent(); len(got) != 0 {
			t.Fatalf("sent before the answer: %q", got)
		}
		if q := strings.Join(v.noteLines(120), " "); !strings.Contains(ansi.Strip(q), "/compact") {
			t.Errorf("the confirmation does not name the command: %q", ansi.Strip(q))
		}
		_, cmd := v.updateKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
		if cmd == nil {
			t.Fatal("y sent nothing")
		}
		msg := runCmd(t, cmd, 10*time.Second)
		if got := sent(); len(got) != 1 || got[0] != "/compact" {
			t.Fatalf("sent %q, want exactly /compact", got)
		}
		v.updateMsg(msg)
		if v.composer.Value() != "a draft I am still writing" {
			t.Errorf("the draft became %q", v.composer.Value())
		}
	})

	t.Run("n declines", func(t *testing.T) {
		client, sent := sendRecorder(t)
		v := gaugeChat(t, usageTurn(1, 190_000, 200_000))
		v.client = client
		v.compactCmds = map[string]string{"claude": "/compact"}
		v.updateKey(registryKey(t, "alt+k"))
		if _, cmd := v.updateKey(tea.KeyPressMsg{Code: 'n', Text: "n"}); cmd != nil || v.compacting {
			t.Fatal("n did not decline")
		}
		if got := sent(); len(got) != 0 {
			t.Fatalf("sent %q", got)
		}
	})

	t.Run("not while a turn runs", func(t *testing.T) {
		v := gaugeChat(t, usageTurn(1, 190_000, 200_000))
		v.compactCmds = map[string]string{"claude": "/compact"}
		v.chat.State = "running"
		for _, b := range v.liveBindings(bindingsFor(ctxChat)) {
			if b.key == "alt+k" {
				t.Error("the compact key is offered while a turn runs")
			}
		}
		v.updateKey(registryKey(t, "alt+k"))
		if v.compacting || !v.noteBad {
			t.Fatal("alt+k asked to compact a running chat")
		}
	})
}

// TestChatGaugeAfterCompact: claude reports no context size for a /compact
// turn, so the gauge must not fall back to the hot pre-compact figure and
// offer to compact again; it is empty until the next turn reports, and that
// turn's figure is the one drawn (review F2).
func TestChatGaugeAfterCompact(t *testing.T) {
	hot := usageTurn(1, 190_000, 200_000)
	compact := usageTurn(2, 0, 0)
	compact.Prompt = "/compact"
	withArgs := compact
	withArgs.Prompt = "/compact keep the API notes"
	running := compact
	running.State, running.EndedAt = "running", nil
	after := usageTurn(3, 30_000, 200_000)
	notCompact := compact
	notCompact.Prompt = "/compaction notes?"

	for _, tc := range []struct {
		name  string
		turns []apiclient.ChatTurn
		cmd   string
		want  string
	}{
		{"compact turn", []apiclient.ChatTurn{hot, compact}, "/compact", ""},
		{"compact with instructions", []apiclient.ChatTurn{hot, withArgs}, "/compact", ""},
		{"compact still running", []apiclient.ChatTurn{hot, running}, "/compact", ""},
		{"turn after compact", []apiclient.ChatTurn{hot, compact, after}, "/compact", "ctx ▰▱▱▱▱ 15%"},
		{"not the command", []apiclient.ChatTurn{hot, notCompact}, "/compact", "! ctx ▰▰▰▰▰ 95% · alt+k compact"},
		{"compact command unknown", []apiclient.ChatTurn{hot, compact}, "", "! ctx ▰▰▰▰▰ 95% · alt+k compact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := chatGauge(tc.turns, true, tc.cmd, "alt+k")
			if got := ansi.Strip(g); got != tc.want {
				t.Errorf("gauge = %q, want %q", got, tc.want)
			}
		})
	}
}
