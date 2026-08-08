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
func (s *Server) startStream(
	w http.ResponseWriter, r *http.Request, replay store.EventFilter, cursor int64,
) (*http.ResponseController, int64, bool) {
	var backlog []store.Event
	if cursor > 0 {
		evs, err := s.deps.Store.ListEvents(r.Context(), replay)
		if err != nil {
			s.internalError(w, "list events", err)
			return nil, 0, false
		}
		backlog = evs
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)

	lastID := cursor
	for i := range backlog {
		if !writeSSEEvent(w, &backlog[i]) {
			return nil, 0, false
		}
		lastID = backlog[i].ID
	}
	if err := rc.Flush(); err != nil {
		return nil, 0, false
	}
	return rc, lastID, true
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
