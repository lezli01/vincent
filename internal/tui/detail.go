package tui

import (
	"context"
	"sort"
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

// detailFocus is which pane the keyboard drives.
type detailFocus int

const (
	focusTimeline detailFocus = iota
	focusOutput
)

// Detail messages.
type (
	// detailLoadedMsg carries a completed task fetch.
	detailLoadedMsg struct {
		id   int64
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
	// screen. The detail view holds a live subscription and must not keep it
	// open for a task nobody is watching.
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

	// selectedRun tracks the attempt under the cursor by id, so a refresh
	// that adds or reorders rows cannot silently select a different one.
	selectedRun int64
	focus       detailFocus
	active      bool

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

	following bool
	newLines  int
	// outputDirty marks the rendered content stale; the pane rebuilds on the
	// next frame rather than on every appended chunk.
	outputDirty bool
	builtWidth  int

	notes      <-chan apiclient.Note
	stopStream context.CancelFunc
	streamID   int64

	refreshPending bool
	width, height  int
}

func newDetail(ctx context.Context) *detail {
	return &detail{ctx: ctx, now: time.Now, vp: viewport.New(), following: true}
}

func (d *detail) title() string { return "Task detail" }

// setClient wires the view to a connected daemon. Called again on reconnect,
// which is when a fresh load and a fresh subscription are exactly right.
func (d *detail) setClient(c *apiclient.Client) tea.Cmd {
	d.client = c
	if d.taskID == 0 {
		return nil
	}
	return tea.Batch(d.loadCmd(), d.syncStream())
}

func (d *detail) update(msg tea.Msg) (view, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
		return d, nil
	case selectTaskMsg:
		return d, d.open(msg.id)
	case viewActivatedMsg:
		if msg.id == viewDetail {
			d.active = true
			return d, tea.Batch(d.loadCmd(), d.syncStream())
		}
		return d, nil
	case viewDeactivatedMsg:
		if msg.id == viewDetail {
			d.active = false
			return d, d.syncStream()
		}
		return d, nil
	case detailLoadedMsg:
		return d, d.applyLoaded(msg)
	case detailTranscriptMsg:
		d.applyTranscript(msg)
		return d, nil
	case detailRefreshMsg:
		if msg.id != d.taskID {
			return d, nil
		}
		d.refreshPending = false
		return d, d.loadCmd()
	case noteMsg:
		// The global stream drives refreshes; the per-task stream is for live
		// output. One refresh trigger, not two.
		return d, d.updateGlobalNote(msg.note)
	case taskNoteMsg:
		return d, d.updateTaskNote(msg)
	case taskStreamDoneMsg:
		return d, nil
	case tea.KeyPressMsg:
		return d.updateKey(msg)
	}
	return d, nil
}

// open switches the view to a task, discarding everything about the last one.
func (d *detail) open(id int64) tea.Cmd {
	if id == d.taskID {
		return nil
	}
	d.taskID = id
	d.task = apiclient.TaskDetail{}
	d.loaded, d.loadErr = false, nil
	d.selectedRun, d.displayRun = 0, 0
	d.resetOutput()
	d.buffer = nil // held output belongs to the task being left
	d.focus = focusTimeline
	return tea.Batch(d.loadCmd(), d.syncStream())
}

// resetOutput clears the pane for a different attempt. It deliberately keeps
// the held-chunk buffer: switching to a newly started attempt is exactly the
// case those chunks were held for, and drainBuffer discards whatever belongs
// to another attempt.
func (d *detail) resetOutput() {
	d.records = nil
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
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		task, err := client.GetTask(ctx, id)
		return detailLoadedMsg{id: id, task: task, err: err}
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

	runs := d.attempts()
	if len(runs) == 0 {
		d.selectedRun = 0
		return nil
	}
	if wasLive || d.runIndex(d.selectedRun) < 0 {
		d.selectedRun = runs[len(runs)-1].ID
	}
	return d.syncOutput()
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
	if len(d.records) > maxRecords {
		d.records = d.records[len(d.records)-maxRecords:]
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
// while this view is on screen with a task.
func (d *detail) syncStream() tea.Cmd {
	want := int64(0)
	if d.active && d.client != nil {
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
	d.notes = d.client.StreamTask(ctx, want, apiclient.StreamOptions{})
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

func (d *detail) updateKey(msg tea.KeyPressMsg) (view, tea.Cmd) {
	switch msg.String() {
	case "tab":
		if d.focus == focusTimeline {
			d.focus = focusOutput
		} else {
			d.focus = focusTimeline
		}
		return d, nil
	case "f":
		d.setFollowing(true)
		return d, nil
	case "esc":
		return d, func() tea.Msg { return selectViewMsg{id: viewBoard} }
	}

	if d.focus == focusTimeline {
		switch msg.String() {
		case "up", "k":
			return d, d.moveSelection(-1)
		case "down", "j":
			return d, d.moveSelection(1)
		}
		return d, nil
	}

	// Output pane: scrolling away from the bottom drops follow, and coming
	// back to it re-arms — the pane's own behavior, not a separate mode.
	switch msg.String() {
	case "G", "end":
		d.setFollowing(true)
		return d, nil
	case "j", "down":
		d.vp.ScrollDown(1)
	case "k", "up":
		d.vp.ScrollUp(1)
	default:
		var cmd tea.Cmd
		d.vp, cmd = d.vp.Update(msg)
		d.syncFollowToViewport()
		return d, cmd
	}
	d.syncFollowToViewport()
	return d, nil
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

// attempts returns every attempt in timeline order: by step, then by attempt
// within the step.
func (d *detail) attempts() []apiclient.StepRun {
	runs := make([]apiclient.StepRun, len(d.task.Steps))
	copy(runs, d.task.Steps)
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].StepIndex != runs[j].StepIndex {
			return runs[i].StepIndex < runs[j].StepIndex
		}
		return runs[i].Attempt < runs[j].Attempt
	})
	return runs
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

// capturesInput reports whether the view is consuming raw keystrokes. The
// detail view has no text field in PR J; the answer form arrives with PR K.
func (d *detail) capturesInput() bool { return false }
