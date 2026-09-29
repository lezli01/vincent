package tui

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// identifier is a snake_case token: what §15's display rule (task 129.7)
// keeps out of the TUI's prose.
var identifier = regexp.MustCompile(`[a-z]+_[a-z_]+`)

// faintRunes is s with its escape sequences removed, beside whether each
// remaining rune was drawn faint. It reads only SGR sequences, which is all
// lipgloss writes, and tracks the one attribute the display rule is about.
func faintRunes(s string) (string, []bool) {
	var plain strings.Builder
	var faint []bool
	on := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				for _, p := range strings.Split(s[i+2:j], ";") {
					switch p {
					case "", "0", "22":
						on = false
					case "2":
						on = true
					}
				}
			}
			i = j + 1
			continue
		}
		r, size := rune(s[i]), 1
		if s[i] >= 0x80 {
			for _, rr := range s[i:] {
				r = rr
				break
			}
			size = len(string(r))
		}
		plain.WriteRune(r)
		for range size {
			faint = append(faint, on)
		}
		i += size
	}
	return plain.String(), faint
}

// proseIdentifiers are the snake_case tokens in frame that are not the faint
// raw code directly after its title (`check failed · check_failed`).
func proseIdentifiers(frame string) []string {
	plain, faint := faintRunes(frame)
	var out []string
	for _, loc := range identifier.FindAllStringIndex(plain, -1) {
		dim := true
		for i := loc[0]; i < loc[1]; i++ {
			dim = dim && faint[i]
		}
		if !dim || !strings.HasSuffix(plain[:loc[0]], reasonSeparator) {
			out = append(out, plain[loc[0]:loc[1]])
		}
	}
	return out
}

func assertNoProseIdentifiers(t *testing.T, surface, frame string) {
	t.Helper()
	if bad := proseIdentifiers(frame); len(bad) > 0 {
		t.Errorf("%s renders %q as prose:\n%s", surface, bad, ansi.Strip(frame))
	}
}

// The scan itself: it must catch a bare or an undimmed code, or passing it
// proves nothing.
func TestProseIdentifierScanCatchesRawCodes(t *testing.T) {
	for frame, want := range map[string]int{
		styleBad.Render("check_failed"):                                      1,
		styleBad.Render("check failed · check_failed"):                       1,
		styleDim.Render("check_failed"):                                      1,
		"blocked on " + styleDim.Render("worktree_dirty"):                    1,
		renderReason("check_failed", styleBad):                               0,
		styleDim.Render("  #1 · blocked at x · check failed · check_failed"): 0,
	} {
		if got := proseIdentifiers(frame); len(got) != want {
			t.Errorf("proseIdentifiers(%q) = %q, want %d", frame, got, want)
		}
	}
}

func TestStateWordsAndTheAttentionBadge(t *testing.T) {
	for state, want := range map[string]string{
		stateAwaitingInput:    "! awaiting input",
		stateAwaitingGate:     "! awaiting approval",
		stateBlocked:          "! blocked",
		stateAwaitingChildren: "waiting on lanes",
		stateRunning:          "running",
		stateQueued:           "queued",
	} {
		if got := stateLabel(state); got != want {
			t.Errorf("stateLabel(%s) = %q, want %q", state, got, want)
		}
	}
	// The longest label fits the column on one line.
	if w := ansi.StringWidth(stateLabel(stateAwaitingGate)); w > widthState {
		t.Errorf("%q is %d cells, over widthState %d", stateLabel(stateAwaitingGate), w, widthState)
	}
	lines := wrapCellLines(stateLabel(stateAwaitingGate), widthState, boardRowLines)
	if len(lines) != 1 {
		t.Errorf("! awaiting approval wraps to %d lines in the STATE column", len(lines))
	}
}

func TestReasonRendersTitleThenDimCode(t *testing.T) {
	got := renderReason("check_failed", styleBad)
	if plain := ansi.Strip(got); plain != "check failed · check_failed" {
		t.Errorf("renderReason = %q, want the title then the code", plain)
	}
	if !strings.Contains(got, styleDim.Render(" · check_failed")) {
		t.Errorf("the raw code is not dim: %q", got)
	}
	// An unknown code is its own title and renders once, not `x · x`.
	if plain := ansi.Strip(renderReason("from_the_future", styleBad)); plain != "from_the_future" {
		t.Errorf("unknown reason renders %q, want it once", plain)
	}
	if got := reasonText("from_the_future"); got != "from_the_future" {
		t.Errorf("reasonText of an unknown code = %q", got)
	}
}

// A blocked board row names its reason only out of a surplus (task 129.7
// decision 2): wide, the cell reads `! blocked · check failed`; at 80 columns
// and on the default 120-column grouped board, where there is no surplus, the
// clause is shed. Either way the cell is one line.
func TestBlockedBoardRowShowsItsReasonOnlyWhenItFits(t *testing.T) {
	reason := "check_failed"
	task := apiclient.Task{ID: 1, State: stateBlocked, BlockReason: &reason}
	content := contentOf([]boardRow{{task: task}})
	if content.stateWant != ansi.StringWidth("! blocked · check failed") {
		t.Fatalf("stateWant = %d", content.stateWant)
	}
	for _, tc := range []struct {
		name  string
		width int
		g     grouping
		want  string
	}{
		{"wide", 220, nil, "! blocked · check failed"},
		{"80 columns", 80, nil, "! blocked"},
		{"120 grouped", 120, defaultGrouping(), "! blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cols, _ := boardColumns(tc.width, tc.g, false, content)
			width := columnWidth(cols, "STATE")
			cell := stateCell(task, width)
			if cell.text != tc.want {
				t.Errorf("state cell at %d = %q, want %q (STATE is %d wide)", tc.width, cell.text, tc.want, width)
			}
			if lines := wrapCellLines(cell.text, width, boardRowLines); len(lines) != 1 {
				t.Errorf("the state cell takes %d lines, want 1: %q", len(lines), lines)
			}
			total := 0
			for _, c := range cols {
				total += c.Width + colPadding
			}
			if total > tc.width {
				t.Errorf("columns total %d at %d", total, tc.width)
			}
		})
	}
	// With no blocked row, STATE never widens.
	cols, _ := boardColumns(220, nil, false, boardContent{})
	if w := columnWidth(cols, "STATE"); w != widthState {
		t.Errorf("STATE is %d wide with nothing to say, want %d", w, widthState)
	}
}

// TestNoIdentifierIsRenderedAsProse is the identifier scan: the surfaces a
// reason or a state reaches, seeded with every shape of it, render no
// snake_case token except the faint code beside its title.
func TestNoIdentifierIsRenderedAsProse(t *testing.T) {
	check := "check_failed"
	blocked := apiclient.Task{ID: 1, State: stateBlocked, BlockReason: &check}
	frames := map[string]string{
		"header blocked": renderDetailState(blocked),
		"header input":   renderDetailState(apiclient.Task{State: stateAwaitingInput}),
		"header gate":    renderDetailState(apiclient.Task{State: stateAwaitingGate}),
		"board blocked":  stateLabelFrame(blocked),
		"board gate":     stateLabelFrame(apiclient.Task{State: stateAwaitingGate}),
		"board input":    stateLabelFrame(apiclient.Task{State: stateAwaitingInput}),
		"board parent": stateLabelFrame(apiclient.Task{State: stateAwaitingChildren, Children: &apiclient.ChildrenRollup{
			Total: 5, Settled: 2, Blocked: []int64{7},
			ByState: map[string]int{stateBlocked: 1, stateAwaitingInput: 1, stateRunning: 1, stateDone: 2},
		}}),
	}

	// The attempt row: a failed attempt, a skipped one, and a status line.
	d := newTestDetail(t)
	d.task.Task = blocked
	failed := attempt(1, 0, 1, "implement", "failed", false)
	failed.FailureReason = &check
	failed.StatusMessage = ptr("3 tests red in internal/store")
	skipped := attempt(2, 1, 1, "docs", "skipped", false)
	cond := "condition"
	skipped.SkipReason = &cond
	loopBlocked := attempt(3, 2, 1, "green", "failed", false)
	loopLimit := "loop_limit"
	loopBlocked.FailureReason = &loopLimit
	d.task.Steps = []apiclient.StepRun{failed, skipped, loopBlocked}
	for _, r := range d.task.Steps {
		frames["attempt "+strconv.FormatInt(r.ID, 10)] = strings.Join(d.attemptLines(r, false), "\n")
	}

	// Step Details' outcome facts.
	frames["step details"] = strings.Join(stepOutcomeLines(failed, fixedNow, 120), "\n")

	// The repair form's subject.
	f := newRepairForm(1, check, "implement")
	frames["repair subject"] = strings.Join(f.lines(120)[:1], "\n")

	// A lane-blocked parent: the blame header and the fan_out step row.
	v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)
	frames["blame header"] = strings.Join(v.detail.headerLines(), "\n")
	frames["blame step"] = strings.Join(v.detail.attemptLines(v.detail.task.Steps[0], false), "\n")
	v.laneSel = 0
	frames["lane strip"] = v.renderLaneSelector(200)

	for surface, frame := range frames {
		assertNoProseIdentifiers(t, surface, frame)
	}
}

// stateLabelFrame is a board state cell styled the way the board draws it.
func stateLabelFrame(t apiclient.Task) string {
	cell := stateCell(t, 60)
	if cell.render != nil {
		return cell.render(cell.text)
	}
	return cell.style.Render(cell.text)
}

// Task Details is the one allowed exception: its state section keeps the raw
// identifier, undimmed, for copy-paste into the CLI (task 049 decision 3).
func TestTaskDetailsKeepsTheRawState(t *testing.T) {
	d := taskDetailFixture(t)
	d.task.State = stateAwaitingInput
	v := newTaskView(d)
	plain, faint := faintRunes(strings.Join(v.detailLines(120), "\n"))
	at := strings.Index(plain, "awaiting_input")
	if at < 0 {
		t.Fatalf("Task Details does not carry the raw state:\n%s", plain)
	}
	for i := at; i < at+len("awaiting_input"); i++ {
		if faint[i] {
			t.Fatalf("the raw state in Task Details is dim:\n%s", plain)
		}
	}
}

// The status message stays apart from the reason (task 036 decision 6): it
// is never run through the catalogue, and never joined into a reason clause.
func TestStatusMessageIsNeverAReasonClause(t *testing.T) {
	d := newTestDetail(t)
	check := "check_failed"
	r := attempt(1, 0, 1, "implement", "failed", false)
	r.FailureReason = &check
	r.StatusMessage = ptr("check_failed on purpose")
	d.task.Steps = []apiclient.StepRun{r}
	got := strings.Join(d.attemptLines(r, false), "\n")
	if !strings.Contains(got, styleStatus.Render(statusGlyph+" check_failed on purpose")) {
		t.Errorf("the status message lost its own style or was rewritten:\n%q", got)
	}
	if strings.Contains(ansi.Strip(got), "check failed on purpose") {
		t.Errorf("the status message went through the catalogue:\n%s", ansi.Strip(got))
	}
}

// The glossary (task 129.7 decision 6), held over every label, hint and
// palette section the registry and the palette carry.
func TestRegistryCopyUsesTheGlossary(t *testing.T) {
	banned := []*regexp.Regexp{
		regexp.MustCompile(`\btask view\b`),
		regexp.MustCompile(`\btask's views\b`),
		regexp.MustCompile(`\btab views\b`),
		regexp.MustCompile(`\b(pass|passes)\b`),
		regexp.MustCompile(`\btiers?\b`),
		regexp.MustCompile(`\b(a|the|this|each|every) run\b`),
		regexp.MustCompile(`needing a human`),
	}
	for _, b := range registry() {
		for _, text := range []string{b.label, b.hint} {
			for _, re := range banned {
				if re.MatchString(text) {
					t.Errorf("%q (key %s) uses %q, outside the glossary", text, b.key, re.String())
				}
			}
		}
	}
	groups := map[string]bool{}
	for _, e := range paletteEntries(ctxTasks, everyAction, true, true, true, nil, nil) {
		groups[e.group] = true
	}
	if groups["views"] || !groups["screens"] {
		t.Errorf("palette groups = %v, want the takeover group named screens", groups)
	}
}
