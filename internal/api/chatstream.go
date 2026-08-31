package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// handleChatEvents is GET /v1/chats/{id}/events (§13.3): one chat's durable
// events interleaved with its ephemeral live output, the per-task stream's
// exact shape for a chat.
//
// The filter is the one internal/store's chatEvent already promises. A chat
// event carries no TaskID — a chat is not a task, and a per-task stream must
// never deliver one — so this narrows on the payload's `id` instead, which is
// why matchesChat unmarshals rather than comparing a column. The output
// subscription rides chatrun.ChatOutputKey, the negative half of the broker's
// int64 key space, so a task subscriber can never be handed a chat's bytes.
//
// Last-Event-ID resumes the durable events only; live-output catch-up is
// GET /v1/chats/{id}/turns/{seq}/transcript, then follow.
func (s *Server) handleChatEvents(w http.ResponseWriter, r *http.Request) {
	if s.deps.Broker == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "event streaming is not available")
		return
	}
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	cursor, ok := lastEventID(w, r)
	if !ok {
		return
	}

	sub := s.deps.Broker.SubscribeEvents(eventSubBuffer)
	defer sub.Close()
	out := s.deps.Broker.SubscribeOutput(chatrun.ChatOutputKey(chat.ID), outputSubBuffer)
	defer out.Close()

	// The replay filter is by type, not by id: the events table has no chat
	// column (the id lives in the payload), so the store narrows to the
	// chat.* family and matchesChat does the rest. A replay therefore reads
	// only chat rows, never every task event behind the cursor.
	rc, lastID, ok := s.startStream(w, r, store.EventFilter{
		AfterID: cursor, Types: store.ChatEventTypes(),
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
			if ev.ID <= lastID || !matchesChat(ev, chat.ID) {
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

// matchesChat reports whether a durable event belongs to this chat. Only the
// chat.* family can, and within it the subject is the payload's `id`.
func matchesChat(ev *store.Event, chatID int64) bool {
	if !store.IsChatEvent(ev.Type) {
		return false
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(ev.Payload, &body); err != nil {
		return false
	}
	return body.ID == chatID
}

// handleChatTurnTranscript is GET /v1/chats/{id}/turns/{seq}/transcript
// (§13.2): one turn's durable record, with the step transcript's byte range
// semantics — `offset` and `tail` are mutually exclusive, the body covers
// whole records, and X-Next-Offset is where the next fetch resumes.
//
// There is no run_id analogue because a chat turn *is* its run: the turn's
// 1-based `seq` names both the row and the `{seq}.jsonl` file beside it, and
// the chunks on the stream carry `turn_id` + `offset`, which is the pair a
// client seams against.
func (s *Server) handleChatTurnTranscript(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	seq, err := strconv.Atoi(r.PathValue("seq"))
	if err != nil || seq <= 0 {
		writeError(w, http.StatusNotFound, CodeNotFound, "chat turn sequence numbers are positive integers")
		return
	}
	turns, err := s.deps.Store.ListChatTurns(r.Context(), chat.ID)
	if err != nil {
		s.internalError(w, "list chat turns", err)
		return
	}
	var turn *store.ChatTurn
	for i := range turns {
		if turns[i].Seq == seq {
			turn = &turns[i]
			break
		}
	}
	if turn == nil {
		writeError(w, http.StatusNotFound, CodeNotFound,
			fmt.Sprintf("turn %d not found on chat %d", seq, chat.ID))
		return
	}

	rng, ok := transcriptRange(w, r)
	if !ok {
		return
	}
	// The path is derived, not stored: chat turns have no TranscriptPath
	// column because their name is a pure function of the two ids, exactly
	// as internal/chatrun computes it when it opens the writer.
	path := filepath.Join(
		s.deps.Dirs.Data, "transcripts", worktree.ChatOwner(chat.ID).Dir(),
		fmt.Sprintf("%d.jsonl", turn.Seq))
	f, err := os.Open(path) //nolint:gosec // the path is built from two ids
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "transcript file is gone (pruned or removed)")
		return
	}
	defer func() { _ = f.Close() }()
	s.serveTranscriptFile(w, f, rng, chat.Agent, turn.ID)
}

// transcriptRangeSpec is a validated ?offset=/?tail=/?format= triple.
type transcriptRangeSpec struct {
	offset     int64
	tail       int64
	hasTail    bool
	normalized bool
}

// transcriptRange parses the query both transcript routes share. Offset and
// tail are mutually exclusive: asking for a position and a window at once has
// no single answer, so it is a client error rather than a precedence rule.
func transcriptRange(w http.ResponseWriter, r *http.Request) (transcriptRangeSpec, bool) {
	q := r.URL.Query()
	rawOffset, rawTail := q.Get("offset"), q.Get("tail")
	var spec transcriptRangeSpec
	if rawOffset != "" && rawTail != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"offset and tail are mutually exclusive")
		return spec, false
	}
	var err error
	if rawOffset != "" {
		spec.offset, err = strconv.ParseInt(rawOffset, 10, 64)
		if err != nil || spec.offset < 0 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "offset must be a non-negative integer")
			return spec, false
		}
	}
	if rawTail != "" {
		spec.tail, err = strconv.ParseInt(rawTail, 10, 64)
		if err != nil || spec.tail < 0 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "tail must be a non-negative integer")
			return spec, false
		}
		spec.hasTail = true
	}
	switch format := q.Get("format"); format {
	case "", "raw":
	case "normalized":
		spec.normalized = true
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"format must be raw or normalized")
		return spec, false
	}
	return spec, true
}

// transcriptFile is what serveTranscriptFile needs of an open transcript:
// random access plus a size. *os.File satisfies it.
type transcriptFile interface {
	io.ReaderAt
	Stat() (os.FileInfo, error)
}

// serveTranscriptFile writes the requested byte range of an open transcript,
// snapped to record boundaries at both ends, with X-Next-Offset naming where
// the next fetch resumes (§13.2). subject names the run in a log line.
func (s *Server) serveTranscriptFile(
	w http.ResponseWriter, f transcriptFile, rng transcriptRangeSpec, agentName string, subject int64,
) {
	fi, err := f.Stat()
	if err != nil {
		s.internalError(w, "stat transcript", err)
		return
	}
	size := fi.Size()

	end, err := lineBoundary(f, size)
	if err != nil {
		s.internalError(w, "scan transcript", err)
		return
	}
	start := min(rng.offset, size)
	if rng.hasTail {
		if start, err = lineBoundary(f, size-rng.tail); err != nil {
			s.internalError(w, "scan transcript", err)
			return
		}
	}
	start = min(start, end)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Next-Offset", strconv.FormatInt(end, 10))
	w.WriteHeader(http.StatusOK)
	section := io.NewSectionReader(f, start, end-start)
	if !rng.normalized {
		_, _ = io.Copy(w, section)
		return
	}
	if err := normalizeTranscript(w, section, s.transcriptParser(agentName)); err != nil {
		// The status is already on the wire; the client sees a short body and
		// resumes from the offset it was given, which is still line-aligned.
		s.deps.Logger.Error("normalize transcript", "run_id", subject, "error", err)
	}
}
