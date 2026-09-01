package tui

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

const (
	// maxRecords bounds the output pane's memory. §18 allows an agent to emit
	// gigabytes; the transcript on disk stays the complete record and the
	// pane keeps the end of it.
	maxRecords = 5000
	// maxBufferedChunks bounds chunks held for an attempt the view has not
	// fetched yet. The hold lasts one refetch, so this is a safety valve, not
	// a queue.
	maxBufferedChunks = 500
	// detailRefreshDebounce coalesces a burst of events for this task into
	// one refetch, matching the board.
	detailRefreshDebounce = 150 * time.Millisecond
)

// detailFocus is which pane the keyboard drives. The shell maps its panel
// focus onto it; the answer form is a popup the shell owns (T3.10), not a
// focus target.
type detailFocus int

const (
	focusTimeline detailFocus = iota
	focusOutput
)

// detailTab is which pane the lower half shows.
type detailTab int

const (
	tabOutput detailTab = iota
	tabDiff
)

// Detail messages.
type (
	// detailLoadedMsg carries a completed task fetch. seq orders concurrent
	// fetches — an older response landing late must not clobber a newer one
	// (zero = untracked, for tests that build the message directly).
	detailLoadedMsg struct {
		id   int64
		seq  uint64
		task apiclient.TaskDetail
		err  error
	}
	// detailTranscriptMsg carries one attempt's transcript window.
	detailTranscriptMsg struct {
		runID   int64
		records []apiclient.TranscriptRecord
		next    int64
		err     error
	}
	// detailRefreshMsg fires when the debounce window closes.
	detailRefreshMsg struct{ id int64 }
	// taskNoteMsg is one note from the per-task stream.
	taskNoteMsg struct {
		taskID int64
		note   apiclient.Note
	}
	// taskStreamDoneMsg reports the per-task note channel closed.
	taskStreamDoneMsg struct{}
	// viewActivatedMsg and viewDeactivatedMsg tell a view it came on or off
	// screen. The shell translates them for the detail sub-model, whose live
	// subscription must not stay open for a task nobody is watching.
	viewActivatedMsg   struct{ id viewID }
	viewDeactivatedMsg struct{ id viewID }
)

// detail is the §15 task-detail view: the step timeline above, the output of
// the selected attempt below. PR K adds the diff tab and the action bar to
// the same shell.
type detail struct {
	ctx    context.Context
	client *apiclient.Client
	now    func() time.Time

	taskID  int64
	task    apiclient.TaskDetail
	loaded  bool
	loadErr error
	// stateHint is the board row's state at open time: it decides whether
	// the subscription opens before the authoritative fetch lands (§13.3 —
	// only a running task has live output worth a stream).
	stateHint string

	// selectedRun tracks the attempt under the cursor by id, so a refresh
	// that adds or reorders rows cannot silently select a different one.
	selectedRun int64
	focus       detailFocus
	tab         detailTab
	active      bool

	// actions is the §6 action bar — the shell's shared instance, rendered
	// by the footer (T3.12); form is the §7.4 answer form, present exactly
	// while the task is parked on a request.
	actions *actionBar
	form    *answerForm
	// repair is the §6 repair form (task 025). Unlike form it is never
	// synthesized from task state: a human opens it with a key, and it closes
	// when they submit or escape.
	repair *repairForm
	// followUp is the §6 follow-up form (task 027), opened the same way and
	// on the same terms as the repair form — by a human pressing a key on a
	// task the daemon offers the action on.
	followUp *followUpForm
	diff     diffPane
	// exec runs $EDITOR for edit+retry, injected so the path is testable
	// without a terminal.
	exec execFunc

	vp viewport.Model
	// displayRun is the attempt the output pane is showing; records are its
	// transcript, nextOffset the position to resume from.
	displayRun   int64
	records      []apiclient.TranscriptRecord
	nextOffset   int64
	fetching     bool
	fetchErr     error
	noTranscript bool
	truncated    bool
	// buffer holds chunks for an attempt not yet fetched — a step that
	// advanced while the refetch was in flight. Dropping them would lose the
	// first moments of every step.
	buffer []apiclient.OutputNote

	// level is how much of each record the output pane shows (§15 `v`). It
	// is a pointer to the session's one holder rather than a value owned
	// here: switching attempts or tasks must not silently reset what a reader
	// chose to look at, and neither must walking into the chat workspace and
	// back (task 071 decision 3).
	level *levelHolder
	// raw is the session's rendered/raw choice, shared with the chat
	// workspace the same way the level is (task 076 decision 2).
	raw *rawHolder

	following bool
	newLines  int
	// outputDirty marks the rendered content stale; the pane rebuilds on the
	// next frame rather than on every appended chunk.
	outputDirty bool
	builtWidth  int
	// seqs is the client-assigned identity of each record in d.records,
	// stamped on ingest and pruned with it, and stamp is where they come
	// from (#291). anchors is the last build's per-line provenance, which is
	// what a paused pane restores its position from.
	stamp   seqStamp
	seqs    []int64
	anchors []lineAnchor
	// mdcache memoizes rendered assistant documents for this pane.
	mdcache mdCache

	notes      <-chan apiclient.Note
	stopStream context.CancelFunc
	streamID   int64
	// openStream opens the per-task stream; injected so tests can count
	// subscriptions without a server (T3.10 done-when: holding `down`
	// across the table opens one, not one per row).
	openStream func(ctx context.Context, id int64, opts apiclient.StreamOptions) <-chan apiclient.Note

	refreshPending bool
	// loadSeq stamps outgoing fetches; appliedSeq is the newest installed.
	loadSeq    uint64
	appliedSeq uint64

	// timelineTop and visibleRuns are the last-rendered timeline geometry,
	// kept so a click can name the attempt on the line it landed on.
	timelineTop int
	visibleRuns []int64

	width, height int
}

func newDetail(ctx context.Context, level *levelHolder, raw *rawHolder) *detail {
	return &detail{
		ctx:       ctx,
		now:       time.Now,
		vp:        viewport.New(),
		level:     level,
		raw:       raw,
		following: true,
		actions:   &actionBar{},
		diff:      newDiffPane(),
		exec:      tea.ExecProcess,
	}
}

// setClient wires the view to a connected daemon. Called again on reconnect,
// which is when a fresh load and a fresh subscription are exactly right.
func (d *detail) setClient(c *apiclient.Client) tea.Cmd {
	d.client = c
	d.openStream = c.StreamTask
	if d.taskID == 0 {
		return nil
	}
	return tea.Batch(d.loadCmd(), d.syncStream())
}

func (d *detail) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
		return nil
	case detailLoadedMsg:
		return d.applyLoaded(msg)
	case taskCreatedMsg:
		// The 201's advisory findings — a catalog-unknown model, say. The
		// task exists and will run, so this is a status line, not an error.
		if len(msg.task.Warnings) > 0 {
			d.actions.setStatus("created with warnings: "+strings.Join(msg.task.Warnings, "; "), false)
		}
		return nil
	case clipboardResultMsg:
		// The copy's outcome, said where every other action's is said: a
		// key press a human made never fails silently (task 076).
		text, bad := msg.notice()
		d.actions.setStatus(text, bad)
		return nil
	case detailTranscriptMsg:
		d.applyTranscript(msg)
		return nil
	case detailRefreshMsg:
		if msg.id != d.taskID {
			return nil
		}
		d.refreshPending = false
		return d.loadCmd()
	case noteMsg:
		// The global stream drives refreshes; the per-task stream is for live
		// output. One refresh trigger, not two.
		return d.updateGlobalNote(msg.note)
	case taskNoteMsg:
		return d.updateTaskNote(msg)
	case taskStreamDoneMsg:
		return nil
	case diffLoadedMsg:
		d.diff.apply(msg)
		return nil
	case actionResultMsg:
		return d.applyAction(msg)
	case editRetryMsg:
		return d.applyEdit(msg)
	case repairAgentsLoadedMsg:
		if d.repair != nil {
			d.repair.applyAgents(msg)
		}
		return nil
	case repairEditMsg:
		if d.repair != nil {
			d.repair.applyEdit(msg)
		}
		return nil
	case followUpLoadedMsg:
		if d.followUp != nil {
			d.followUp.applyLoaded(msg)
		}
		return nil
	case followUpEditMsg:
		if d.followUp != nil {
			d.followUp.applyEdit(msg)
		}
		return nil
	case transcriptOpenedMsg:
		// Nothing to apply — the file was only read. A failed viewer is worth
		// saying, since the terminal handover leaves no other trace of it.
		if msg.err != nil {
			d.actions.setStatus("transcript viewer: "+errString(msg.err), true)
		}
		return nil
	case tea.KeyPressMsg:
		return d.updateKey(msg)
	}
	return nil
}

// applyAction records what an action did and refetches: the response already
// carries the new state, and the refetch picks up everything hanging off it
// (a new step row, a cleared pending request, a 409's real state).
func (d *detail) applyAction(msg actionResultMsg) tea.Cmd {
	if msg.taskID != d.taskID {
		return nil
	}
	d.actions.applyResult(msg)
	if msg.action == apiclient.ActionRepair && d.repair != nil {
		if msg.err != nil {
			// Same reasoning as the answer form: keep what was typed on
			// screen rather than making the human write it again.
			d.repair.submitting = false
			d.repair.err = errString(msg.err)
		} else {
			d.repair = nil
		}
	}
	if msg.action == apiclient.ActionFollowUp && d.followUp != nil {
		if msg.err != nil {
			// Same reasoning as the repair form: keep what was typed on
			// screen rather than making the human write it again.
			d.followUp.submitting = false
			d.followUp.err = errString(msg.err)
		} else {
			d.followUp = nil
		}
	}
	if msg.action == apiclient.ActionAnswer && msg.err != nil && d.form != nil {
		// The daemon refused the answer: keep the form and its typed values on
		// screen, since re-entering them is the last thing a human wants.
		d.form.submitting = false
		d.form.err = errString(msg.err)
	}
	d.refreshPending = false
	return d.loadCmd()
}

// open switches the view to a task, discarding everything about the last
// one. stateHint is the board row's state, deciding whether the stream
// opens before the fetch confirms the task is running.
func (d *detail) open(id int64, stateHint string) tea.Cmd {
	if id == d.taskID {
		return nil
	}
	d.taskID = id
	d.stateHint = stateHint
	d.task = apiclient.TaskDetail{}
	d.loaded, d.loadErr = false, nil
	d.selectedRun, d.displayRun = 0, 0
	d.resetOutput()
	d.buffer = nil // held output belongs to the task being left
	d.focus = focusTimeline
	d.tab = tabOutput
	d.actions.clear()
	d.form, d.repair, d.followUp = nil, nil, nil
	d.diff.openTask(id)
	return tea.Batch(d.loadCmd(), d.syncStream())
}

// deselect drops the task without opening another: the table cursor moved
// and the settle window is still open. The subscription is torn down here,
// immediately — an explicit unsubscribe on move (PR P decision) — while any
// new subscription waits for the settle.
func (d *detail) deselect() {
	d.taskID = 0
	d.stateHint = ""
	d.task = apiclient.TaskDetail{}
	d.loaded, d.loadErr = false, nil
	d.selectedRun, d.displayRun = 0, 0
	d.resetOutput()
	d.buffer = nil
	d.actions.clear()
	d.form, d.repair, d.followUp = nil, nil, nil
	d.syncStream()
}

// resetOutput clears the pane for a different attempt. It deliberately keeps
// the held-chunk buffer: switching to a newly started attempt is exactly the
// case those chunks were held for, and drainBuffer discards whatever belongs
// to another attempt.
func (d *detail) resetOutput() {
	d.records = nil
	d.seqs = nil
	d.anchors = nil
	d.nextOffset = 0
	d.fetching = false
	d.fetchErr = nil
	d.noTranscript = false
	d.truncated = false
	d.following = true
	d.newLines = 0
	d.outputDirty = true
	d.vp.SetContent("")
}

func (d *detail) loadCmd() tea.Cmd {
	client, id := d.client, d.taskID
	if client == nil || id == 0 {
		return nil
	}
	d.loadSeq++
	seq := d.loadSeq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		task, err := client.GetTask(ctx, id)
		return detailLoadedMsg{id: id, seq: seq, task: task, err: err}
	}
}

// scheduleRefresh opens a debounce window, or does nothing if one is open.
func (d *detail) scheduleRefresh() tea.Cmd {
	if d.refreshPending || d.taskID == 0 {
		return nil
	}
	d.refreshPending = true
	id := d.taskID
	return tea.Tick(detailRefreshDebounce, func(time.Time) tea.Msg {
		return detailRefreshMsg{id: id}
	})
}

// applyLoaded installs a fetched task and decides what the cursor and the
// output pane should now be showing.
func (d *detail) applyLoaded(msg detailLoadedMsg) tea.Cmd {
	if msg.id != d.taskID {
		return nil // a fetch for a task the view has already left
	}
	if msg.seq != 0 && msg.seq <= d.appliedSeq {
		return nil // a slower, older fetch landing after a newer one
	}
	if msg.err != nil {
		// Keep what is on screen: a failed refresh is not a lost connection,
		// and blanking a running task's timeline over one 500 destroys the
		// view exactly when it is being watched.
		d.loadErr = msg.err
		return nil
	}
	// Whether the cursor was tracking the live attempt decides whether it
	// follows a step advance or stays where the user put it.
	wasLive := d.selectedRun == 0 || d.runByID(d.selectedRun).Live()

	d.loadErr = nil
	d.loaded = true
	d.task = msg.task
	d.appliedSeq = msg.seq
	d.syncForm()

	runs := d.attempts()
	if len(runs) == 0 {
		d.selectedRun = 0
		// The authoritative state may disagree with the hint the open used —
		// a task that stopped running drops its stream, one that started
		// gains it.
		return d.syncStream()
	}
	if wasLive || d.runIndex(d.selectedRun) < 0 {
		d.selectedRun = runs[len(runs)-1].ID
	}
	return tea.Batch(d.syncOutput(), d.syncStream())
}

// running reports whether the task has live output worth a stream: the
// loaded state when there is one, the board row's hint before that.
func (d *detail) running() bool {
	if d.loaded {
		return d.task.State == stateRunning
	}
	return d.stateHint == stateRunning
}

// syncForm keeps the answer form in step with the task: it exists exactly
// while a request is pending, and survives a refresh that did not change the
// request, so a refetch mid-typing does not discard what was typed.
func (d *detail) syncForm() {
	req, ok, err := d.task.PendingRequest()
	if err != nil || !ok {
		d.form = nil
		return
	}
	if d.form != nil && d.form.sameRequest(req) {
		return
	}
	// The form exists as state; it renders as a popup the human opens. It
	// never steals focus — auto-opening under a keystroke is how an answer
	// gets lost (§15).
	d.form = newAnswerForm(req)
}

// syncOutput makes the output pane match the selected attempt, fetching its
// transcript when the selection moved.
func (d *detail) syncOutput() tea.Cmd {
	if d.selectedRun == 0 || d.selectedRun == d.displayRun {
		return nil
	}
	run := d.runByID(d.selectedRun)
	d.displayRun = d.selectedRun
	d.resetOutput()
	// A manual gate or a skipped step never opened a transcript; there is
	// nothing to fetch and saying so beats an empty pane.
	if run.TranscriptPath == nil {
		d.noTranscript = true
		return nil
	}
	d.following = run.Live()
	d.fetching = true
	return d.transcriptCmd(d.displayRun, apiclient.TranscriptOptions{Tail: apiclient.DefaultTailBytes})
}

func (d *detail) transcriptCmd(runID int64, opts apiclient.TranscriptOptions) tea.Cmd {
	client, taskID := d.client, d.taskID
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		recs, next, err := client.Transcript(ctx, taskID, runID, opts)
		return detailTranscriptMsg{runID: runID, records: recs, next: next, err: err}
	}
}

// applyTranscript installs a fetched window and closes the catch-up seam:
// buffered chunks the fetch already covered are dropped by offset, and the
// rest are appended in order.
func (d *detail) applyTranscript(msg detailTranscriptMsg) {
	if msg.runID != d.displayRun {
		return
	}
	d.fetching = false
	if msg.err != nil {
		d.fetchErr = msg.err
		return
	}
	d.fetchErr = nil
	d.records = msg.records
	d.seqs = d.stamp.take(len(msg.records))
	d.nextOffset = msg.next
	d.drainBuffer()
	d.outputDirty = true
}

// drainBuffer appends held chunks belonging to the displayed attempt that the
// transcript fetch did not already cover.
func (d *detail) drainBuffer() {
	if len(d.buffer) == 0 {
		return
	}
	kept := d.buffer[:0]
	for _, note := range d.buffer {
		if note.RunID != d.displayRun {
			continue // another attempt's output; durable in its own transcript
		}
		if note.Offset <= d.nextOffset {
			continue // the fetch already covered this line
		}
		d.appendChunk(note)
	}
	d.buffer = kept[:0]
}

// appendChunk turns one live chunk into a record and advances the resume
// offset, so a later fetch and this stream cannot double-count a line.
func (d *detail) appendChunk(note apiclient.OutputNote) {
	rec := recordFromChunk(note)
	d.records = append(d.records, rec)
	d.seqs = append(d.seqs, d.stamp.take(1)...)
	if len(d.records) > maxRecords {
		d.records = d.records[len(d.records)-maxRecords:]
		d.seqs = d.seqs[len(d.seqs)-maxRecords:]
		d.truncated = true
	}
	if note.Offset > d.nextOffset {
		d.nextOffset = note.Offset
	}
	if !d.following {
		d.newLines++
	}
}

// updateGlobalNote reacts to the shell's stream: any durable event about this
// task means the timeline may have changed.
func (d *detail) updateGlobalNote(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok || d.taskID == 0 {
		return nil
	}
	if ev.Event.TaskID == nil || *ev.Event.TaskID != d.taskID {
		return nil
	}
	return d.scheduleRefresh()
}

// updateTaskNote handles the per-task stream: live output, and a reconnect
// that means live output was missed while the connection was down.
func (d *detail) updateTaskNote(msg taskNoteMsg) tea.Cmd {
	if msg.taskID != d.streamID {
		return nil // a note from a subscription already torn down
	}
	cmds := []tea.Cmd{waitTaskNote(d.notes, msg.taskID)}
	switch n := msg.note.(type) {
	case apiclient.OutputNote:
		cmds = append(cmds, d.handleChunk(n))
	case apiclient.ConnectedNote:
		// Live output is not replayable, so the only honest catch-up after a
		// gap is to re-read the transcript (§13.3).
		if d.displayRun != 0 && !d.noTranscript {
			d.fetching = true
			cmds = append(cmds, d.transcriptCmd(d.displayRun,
				apiclient.TranscriptOptions{Tail: apiclient.DefaultTailBytes}))
		}
	case apiclient.EventNote, apiclient.DisconnectedNote:
		// Durable events arrive on the shell's stream, which already drives
		// the refresh; a drop is visible there too.
	}
	return tea.Batch(cmds...)
}

// handleChunk routes one live-output chunk. A chunk for the displayed
// attempt appends (unless a fetch is in flight, which owns the seam); a
// chunk for an attempt the view has not seen yet is held and forces an
// immediate refetch rather than being dropped.
func (d *detail) handleChunk(n apiclient.OutputNote) tea.Cmd {
	switch {
	case d.fetching:
		d.hold(n)
		return nil
	case n.RunID == d.displayRun:
		if n.Offset > d.nextOffset {
			d.appendChunk(n)
			d.outputDirty = true
		}
		return nil
	case d.runIndex(n.RunID) < 0:
		// An attempt that started since the last fetch: hold its output and
		// go get the step row that explains it, without the debounce.
		d.hold(n)
		d.refreshPending = false
		return d.loadCmd()
	default:
		return nil // a known attempt the user is not looking at
	}
}

func (d *detail) hold(n apiclient.OutputNote) {
	if len(d.buffer) >= maxBufferedChunks {
		return
	}
	d.buffer = append(d.buffer, n)
}

// syncStream opens or closes the per-task subscription so it exists exactly
// while this sub-model is on screen with a *running* task — a finished or
// parked task has no live output, and its transcript is the durable copy
// (§13.3). The running-only rule plus the shell's settle window is what
// keeps a cursor sweep from opening a stream per row.
func (d *detail) syncStream() tea.Cmd {
	want := int64(0)
	if d.active && d.openStream != nil && d.running() {
		want = d.taskID
	}
	if want == d.streamID {
		return nil
	}
	if d.stopStream != nil {
		d.stopStream()
		d.stopStream, d.notes, d.streamID = nil, nil, 0
	}
	if want == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(d.ctx)
	d.stopStream = cancel
	d.streamID = want
	d.notes = d.openStream(ctx, want, apiclient.StreamOptions{})
	return waitTaskNote(d.notes, want)
}

// waitTaskNote receives the next note as a message; update re-arms it.
func waitTaskNote(ch <-chan apiclient.Note, taskID int64) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		n, ok := <-ch
		if !ok {
			return taskStreamDoneMsg{}
		}
		return taskNoteMsg{taskID: taskID, note: n}
	}
}

func (d *detail) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	// A pending confirmation is a question: it owns the keyboard until it is
	// answered. (The answer form is a popup the shell routes to before the
	// key ever reaches here.)
	if d.actions.capturing() {
		cmd, _ := d.actions.handleKey(msg.String(), d.client, d.target())
		return cmd
	}

	switch msg.String() {
	case "]", "[", "d":
		// The registry names `]` canonical with `[` and `d` as aliases, and
		// all three toggle: with two tabs, "next" and "previous" and "the
		// other one" are the same move. A third tab would have to split them,
		// and this is the place that changes.
		return d.toggleTab()
	case "e":
		return d.openTranscript()
	case "E":
		if d.target().has(apiclient.ActionRetry) {
			return d.editRetry()
		}
		return nil
	case "R":
		// The repair form (§6, task 025). `r` is retry and `E` is edit+retry;
		// `R` is free in the task workspace, where the takeover screens that use
		// it for re-probing never are.
		return d.openRepair()
	case "F":
		// The follow-up form (§6, task 027). `f` is follow-output and is
		// panel-scoped, so the capital is free in the task-action scope.
		return d.openFollowUp()
	case "f":
		d.setFollowing(true)
		return nil
	case "v":
		// Handled here, alongside `d` and `f`, so it works from either focus
		// within the detail view — the key acts on the output pane, and
		// having to focus the pane first to change what it shows is a step
		// nobody would guess at.
		return d.cycleLevel()
	case rawToggleKey:
		return d.toggleRaw()
	case copyPickKey:
		return openCopyPicker(copyDocsFromRecords(d.records, d.recordSeqs()),
			func(seq int64) (string, bool) {
				return resolveDocs(d.records, d.recordSeqs(), seq)
			})
	}

	// Action keys work from any focus: they act on the task, not on a pane.
	if cmd, handled := d.actions.handleKey(msg.String(), d.client, d.target()); handled {
		return cmd
	}

	if d.tab == tabDiff && d.focus == focusOutput {
		return d.diff.updateKey(msg)
	}

	if d.focus == focusTimeline {
		switch msg.String() {
		case "up", "k":
			return d.moveSelection(-1)
		case "down", "j":
			return d.moveSelection(1)
		}
		return nil
	}

	// Output pane: scrolling away from the bottom drops follow, and coming
	// back to it re-arms — the pane's own behavior, not a separate mode.
	switch msg.String() {
	case "G", "end":
		d.setFollowing(true)
		return nil
	case "j", "down":
		d.vp.ScrollDown(1)
	case "k", "up":
		d.vp.ScrollUp(1)
	default:
		var cmd tea.Cmd
		d.vp, cmd = d.vp.Update(msg)
		d.syncFollowToViewport()
		return cmd
	}
	d.syncFollowToViewport()
	return nil
}

// toggleRaw flips the pane between the rendered Markdown and the stored
// source, and rebuilds. Nothing else moves: the records, the offset, the
// level and follow mode are untouched — raw is a way of drawing what is
// already there (task 076 decision 2).
func (d *detail) toggleRaw() tea.Cmd {
	d.raw.toggle()
	d.outputDirty = true
	return nil
}

// cycleLevel advances the session's verbosity and rebuilds the pane.
//
// The level needs no plumbing to outlive a task: the shell builds one holder
// at startup and hands it to every pane that renders records, so nothing
// resets it. That is the whole requirement — a reader who asked for verbose
// keeps it when they open the next task, and when they open a chat.
func (d *detail) cycleLevel() tea.Cmd {
	d.level.cycle()
	d.outputDirty = true
	return nil
}

func (d *detail) setFollowing(on bool) {
	d.following = on
	if on {
		d.newLines = 0
		d.vp.GotoBottom()
	}
}

// syncFollowToViewport keeps follow honest after a manual scroll: the pane is
// following exactly when it is showing the end of the output.
func (d *detail) syncFollowToViewport() {
	if d.vp.AtBottom() {
		d.following = true
		d.newLines = 0
		return
	}
	d.following = false
}

func (d *detail) moveSelection(delta int) tea.Cmd {
	runs := d.attempts()
	if len(runs) == 0 {
		return nil
	}
	i := d.runIndex(d.selectedRun)
	if i < 0 {
		i = len(runs) - 1
	}
	i = min(max(i+delta, 0), len(runs)-1)
	d.selectedRun = runs[i].ID
	return d.syncOutput()
}

// attempts returns every attempt in timeline order: by step, then by
// iteration, then — inside a `parallel` group, whose sub-steps share one step
// index (task 014) — by sub-step, then by attempt. Without the middle tiers a
// structure step's attempts interleave by number and neither a sub-step nor
// an iteration reads as a unit.
//
// Iteration sits above the sub-step tier because a `loop` body is a
// *sequence* run more than once (§7.8): iteration 2's first step belongs
// under iteration 2, not beside iteration 1's copy of itself. Every row
// outside a loop carries iteration 0, so this tier is invisible there.
//
// The order within a group is the server's: ListStepRuns returns rows by
// index, iteration, attempt and id, so the first row of each sub-step appears
// in the order the sub-steps started. A stable sort keeps that, which is as
// close to declaration order as the rows can honestly report.
func (d *detail) attempts() []apiclient.StepRun {
	runs := make([]apiclient.StepRun, len(d.task.Steps))
	copy(runs, d.task.Steps)
	order := map[string]int{}
	for _, r := range runs {
		key := stepRunKey(r)
		if _, seen := order[key]; !seen {
			order[key] = len(order)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].StepIndex != runs[j].StepIndex {
			return runs[i].StepIndex < runs[j].StepIndex
		}
		if runs[i].Iteration != runs[j].Iteration {
			return runs[i].Iteration < runs[j].Iteration
		}
		if runs[i].StepID != runs[j].StepID {
			return order[stepRunKey(runs[i])] < order[stepRunKey(runs[j])]
		}
		return runs[i].Attempt < runs[j].Attempt
	})
	return runs
}

// stepRunKey identifies one step's run history within a task: the index alone
// is not enough once a group's sub-steps, or a loop body's iterations, share
// it.
func stepRunKey(r apiclient.StepRun) string {
	return strconv.Itoa(r.StepIndex) + "/" + strconv.Itoa(r.Iteration) + "/" + r.StepID
}

func (d *detail) runIndex(id int64) int {
	if id == 0 {
		return -1
	}
	for i, r := range d.attempts() {
		if r.ID == id {
			return i
		}
	}
	return -1
}

func (d *detail) runByID(id int64) apiclient.StepRun {
	for _, r := range d.task.Steps {
		if r.ID == id {
			return r
		}
	}
	return apiclient.StepRun{}
}

// capturesInput reports whether the sub-model is consuming raw keystrokes:
// a y/n confirmation. (The answer form's text entry is captured by the
// shell's popup routing before keys reach here.)
func (d *detail) capturesInput() bool {
	return d.actions.capturing()
}

// openRepair opens the repair form for a blocked task, fetching the adapter
// catalog its pickers render. It offers nothing the daemon does not: the
// action has to be on the task's available_actions (§6).
func (d *detail) openRepair() tea.Cmd {
	if !d.target().has(apiclient.ActionRepair) {
		return nil
	}
	reason := ""
	if d.task.BlockReason != nil {
		reason = *d.task.BlockReason
	}
	d.repair = newRepairForm(d.taskID, reason, d.currentStepName())
	d.repair.openEditor = d.editRepairPrompt
	return d.repair.loadAgents(d.client)
}

// openFollowUp opens the follow-up form for a finished task, fetching the
// adapter and workflow catalogs its pickers render. It offers nothing the
// daemon does not: the action has to be on the task's available_actions (§6),
// which is `done` and `aborted` and no other state.
func (d *detail) openFollowUp() tea.Cmd {
	if !d.target().has(apiclient.ActionFollowUp) {
		return nil
	}
	d.followUp = newFollowUpForm(d.taskID, d.task.ProjectID, d.task.State)
	d.followUp.openEditor = d.editFollowUpBody
	return d.followUp.load(d.client)
}

// currentStepName names the step the task is blocked at, for the form's
// subject line; "" when the snapshot has not arrived.
func (d *detail) currentStepName() string {
	step, ok := d.task.Step(d.task.CurrentStep)
	if !ok {
		return ""
	}
	return step.ID
}

// target is the slice of the task the action bar works from.
func (d *detail) target() taskActions {
	return taskActions{id: d.taskID, state: d.task.State, actions: d.task.AvailableActions}
}

// toggleTab switches the lower pane between output and diff. Opening the diff
// tab fetches it; nothing else does, because the endpoint runs git per call
// and following the event stream would mean a subprocess per event. Leaving
// and re-entering the tab is therefore also how a diff is refreshed.
func (d *detail) toggleTab() tea.Cmd {
	if d.tab == tabDiff {
		d.tab = tabOutput
		return nil
	}
	d.tab = tabDiff
	d.diff.openTask(d.taskID)
	return d.diff.fetch(d.client, true)
}

// recordSeqs is the pane's identity slice, stamped if some path installed
// records without going through applyTranscript or appendChunk.
func (d *detail) recordSeqs() []int64 {
	d.seqs = d.stamp.fit(d.seqs, len(d.records))
	return d.seqs
}
