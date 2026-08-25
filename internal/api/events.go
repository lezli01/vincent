package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

const (
	// heartbeatInterval paces the `:heartbeat` comment that keeps idle SSE
	// connections provably alive (§13.3, phase 2 decision).
	heartbeatInterval = 15 * time.Second
	// outputFlushInterval coalesces live output: chunks are written as they
	// arrive but flushed at most this often — §13.3's ~10 Hz. State events
	// flush immediately; they are rare and time-critical.
	outputFlushInterval = 100 * time.Millisecond
	// eventSubBuffer is the durable-event channel depth; overflowing it
	// disconnects the client, which resumes via Last-Event-ID losing nothing.
	eventSubBuffer = 256
	// outputSubBuffer is the live-output channel depth; overflow drops
	// chunks (the transcript is the durable copy).
	outputSubBuffer = 1024
	// replayPageSize is how many events one Last-Event-ID catch-up query
	// reads. It caps what a replay holds and how long it keeps the single
	// SQLite connection, whatever the backlog behind the cursor is; small
	// enough that a page is a few hundred KiB of envelopes, large enough
	// that a realistic catch-up is a handful of queries.
	replayPageSize = 512
)

// eventJSON is the SSE `data:` body of a durable event: the full envelope,
// so a client needs no side channel to interpret the row.
type eventJSON struct {
	ID        int64           `json:"id"`
	TS        string          `json:"ts"`
	Type      string          `json:"type"`
	TaskID    *int64          `json:"task_id,omitempty"`
	ProjectID *int64          `json:"project_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

// handleEvents is GET /v1/events (§13.3): durable state events for every
// task. With Last-Event-ID it replays `id > cursor` from the events table
// and follows live; without one it starts live at the next committed event —
// the stream never replays history unasked (PR D decision).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.deps.Broker == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "event streaming is not available")
		return
	}
	cursor, ok := lastEventID(w, r)
	if !ok {
		return
	}
	var types []string
	if raw := strings.TrimSpace(r.URL.Query().Get("types")); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				types = append(types, t)
			}
		}
	}
	var projectID int64
	if raw := r.URL.Query().Get("project_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "project_id must be an integer")
			return
		}
		projectID = id
	}

	// Subscribe before the replay query so nothing slips through the seam;
	// the id cursor dedupes the overlap (phase 2 decision).
	sub := s.deps.Broker.SubscribeEvents(eventSubBuffer)
	defer sub.Close()

	rc, lastID, ok := s.startStream(w, r, store.EventFilter{
		AfterID: cursor, Types: types, ProjectID: projectID,
	}, cursor)
	if !ok {
		return
	}

	matches := func(ev *store.Event) bool {
		if len(types) > 0 && !slices.Contains(types, ev.Type) {
			return false
		}
		if projectID != 0 && (ev.ProjectID == nil || *ev.ProjectID != projectID) {
			return false
		}
		return true
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, chOpen := <-sub.C:
			if !chOpen {
				return // broker closed (shutdown) or this client fell behind
			}
			if ev.ID <= lastID || !matches(ev) {
				continue
			}
			if !writeSSEEvent(w, ev) || rc.Flush() != nil {
				return
			}
			lastID = ev.ID
		case <-heartbeat.C:
			if !writeHeartbeat(w, rc) {
				return
			}
		}
	}
}

// handleTaskEvents is GET /v1/tasks/{id}/events (§13.3): the task's durable
// events interleaved with its ephemeral live output. Last-Event-ID resumes
// the durable events only; live-output catch-up is the transcript, then
// follow (documented behavior). Output is coalesced onto a ~100 ms flush.
func (s *Server) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	if s.deps.Broker == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "event streaming is not available")
		return
	}
	task, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	cursor, ok := lastEventID(w, r)
	if !ok {
		return
	}

	sub := s.deps.Broker.SubscribeEvents(eventSubBuffer)
	defer sub.Close()
	out := s.deps.Broker.SubscribeOutput(task.ID, outputSubBuffer)
	defer out.Close()

	rc, lastID, ok := s.startStream(w, r, store.EventFilter{
		AfterID: cursor, TaskID: task.ID,
	}, cursor)
	if !ok {
		return
	}

	flushTimer := time.NewTicker(outputFlushInterval)
	defer flushTimer.Stop()
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	dirty := false
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, chOpen := <-sub.C:
			if !chOpen {
				return
			}
			if ev.ID <= lastID || ev.TaskID == nil || *ev.TaskID != task.ID {
				continue
			}
			if !writeSSEEvent(w, ev) || rc.Flush() != nil {
				return
			}
			lastID, dirty = ev.ID, false
		case chunk, chOpen := <-out.C:
			if !chOpen {
				return
			}
			if !writeSSEChunk(w, chunk) {
				return
			}
			dirty = true // the flush timer pushes it (§13.3's ~10 Hz)
		case <-flushTimer.C:
			if dirty {
				if rc.Flush() != nil {
					return
				}
				dirty = false
			}
		case <-heartbeat.C:
			if !writeHeartbeat(w, rc) {
				return
			}
		}
	}
}

// startStream commits the response to SSE: headers, any Last-Event-ID
// replay from the events table, and the first flush. Returns the response
// controller and the last event id written. Validation must be complete —
// nothing can be unwritten past this point.
//
// The replay is paged: event rows are kept indefinitely (§17), so the
// backlog behind a cursor has no ceiling and reading it in one query would
// hold all of it in memory and occupy the daemon's single SQLite connection
// (phase 1 decision) until the last row was scanned. Paging bounds both to
// one page and hands the connection back between pages. §13.3's "miss
// nothing" is untouched — every event behind the cursor is still delivered,
// in id order, just not all at once.
func (s *Server) startStream(
	w http.ResponseWriter, r *http.Request, replay store.EventFilter, cursor int64,
) (*http.ResponseController, int64, bool) {
	// The walk stops at the largest id committed when the stream opened.
	// Without that bound a replay on a busy daemon could page after its own
	// tail indefinitely and never reach the live loop. Events committed past
	// it arrive through the subscription — registered before this call
	// (phase 2 decision) and deduped by the last id returned here — so the
	// seam still delivers each one exactly once.
	var (
		highWater int64
		page      []store.Event
		err       error
	)
	if cursor > 0 {
		if highWater, err = s.deps.Store.MaxEventID(r.Context()); err != nil {
			s.internalError(w, "max event id", err)
			return nil, 0, false
		}
		// The first page is read before the headers so a store failure can
		// still be answered with an error envelope rather than a half-open
		// stream; later pages have nowhere to report to but the connection.
		if page, err = s.replayPage(r, replay, cursor); err != nil {
			s.internalError(w, "list events", err)
			return nil, 0, false
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)

	lastID := cursor
	for {
		for i := range page {
			if !writeSSEEvent(w, &page[i]) {
				return nil, 0, false
			}
			lastID = page[i].ID
		}
		if err := rc.Flush(); err != nil {
			return nil, 0, false
		}
		// A short page means the filtered backlog is exhausted (LIMIT applies
		// after the WHERE), and lastID past the mark means the rest is the
		// subscription's to deliver.
		if len(page) < replayPageSize || lastID >= highWater {
			return rc, lastID, true
		}
		// A client that has gone away stops the walk rather than paging
		// through the remaining backlog for nobody.
		if r.Context().Err() != nil {
			return nil, 0, false
		}
		if page, err = s.replayPage(r, replay, lastID); err != nil {
			s.deps.Logger.Error("list events", "error", err)
			return nil, 0, false
		}
	}
}

// replayPage reads one page of the Last-Event-ID backlog: the events after
// `after` that match the stream's filter, at most replayPageSize of them.
func (s *Server) replayPage(r *http.Request, f store.EventFilter, after int64) ([]store.Event, error) {
	f.AfterID = after
	f.Limit = replayPageSize
	return s.deps.Store.ListEvents(r.Context(), f)
}

// lastEventID reads the SSE resume cursor. Absent means live-only; our ids
// are integers, so anything else is a client error worth naming.
func lastEventID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "Last-Event-ID must be an event id")
		return 0, false
	}
	return id, true
}

// writeSSEEvent writes one durable event frame: id + event + data, the id
// being the client's next Last-Event-ID.
func writeSSEEvent(w io.Writer, ev *store.Event) bool {
	data, err := json.Marshal(eventJSON{
		ID:        ev.ID,
		TS:        ev.TS.UTC().Format(time.RFC3339),
		Type:      ev.Type,
		TaskID:    ev.TaskID,
		ProjectID: ev.ProjectID,
		Payload:   ev.Payload,
	})
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, data)
	return err == nil
}

// writeSSEChunk writes one live-output frame. No id: output is ephemeral
// and not replayable over SSE (§13.3).
func writeSSEChunk(w io.Writer, c events.Chunk) bool {
	data, err := json.Marshal(c.Payload)
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", c.Type, data)
	return err == nil
}

func writeHeartbeat(w io.Writer, rc *http.ResponseController) bool {
	if _, err := io.WriteString(w, ":heartbeat\n\n"); err != nil {
		return false
	}
	return rc.Flush() == nil
}
