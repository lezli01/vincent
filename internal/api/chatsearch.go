package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lezli01/vincent/internal/store"
)

// CodeQueryTooShort is a chat search whose terms are too short for the
// trigram index (task 156 decision 2): refused rather than answered with a
// full scan.
const CodeQueryTooShort = "query_too_short"

// chatSearchHitBody is one chat a search found: the chat as GET /v1/chats
// renders it, and where it matched.
type chatSearchHitBody struct {
	Chat       chatBody              `json:"chat"`
	Matches    []chatSearchMatchBody `json:"matches"`
	MatchCount int                   `json:"match_count"`
}

// chatSearchMatchBody is one place a chat matched. Seq is 0 for the title.
// The snippet is plain text, and Segments is the same text split into runs
// marked as matching or not — the highlight is data, never markup or ANSI a
// client would have to parse (task 156).
type chatSearchMatchBody struct {
	Seq      int                     `json:"seq"`
	Field    string                  `json:"field"`
	Snippet  string                  `json:"snippet"`
	Segments []chatSearchSegmentBody `json:"segments"`
}

type chatSearchSegmentBody struct {
	Text  string `json:"text"`
	Match bool   `json:"match,omitempty"`
}

// handleChatSearch serves GET /v1/chats/search (task 156, #1022, §13.2):
// a full-text search over every chat's title, prompts and final answers.
// `archived` is GET /v1/chats' parameter with GET /v1/chats' values, but
// defaults to `all` — finding a chat that is done with is the point of
// searching (task 156 decision 3).
func (s *Server) handleChatSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	text := q.Get("q")
	var opts store.ChatSearchOptions
	if v := q.Get("project_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "project_id must be an integer")
			return
		}
		opts.ProjectID = &id
	}
	switch v := q.Get("archived"); v {
	case "", "all":
		opts.Archived = store.ArchivedAll
	case "false":
		opts.Archived = store.ArchivedExclude
	case "true":
		opts.Archived = store.ArchivedOnly
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"archived must be one of: false, true, all")
		return
	}
	for name, dst := range map[string]*int{"limit": &opts.Limit, "turns": &opts.TurnsPerChat} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, CodeValidationFailed,
					fmt.Sprintf("%s must be a non-negative integer", name))
				return
			}
			*dst = n
		}
	}
	hits, err := s.deps.Store.SearchChats(r.Context(), text, opts)
	switch {
	case errors.Is(err, store.ErrSearchQueryEmpty):
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "q is required")
		return
	case errors.Is(err, store.ErrSearchQueryTooShort):
		writeError(w, http.StatusBadRequest, CodeQueryTooShort, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	ids := make([]int64, len(hits))
	for i := range hits {
		ids[i] = hits[i].Chat.ID
	}
	counts, err := s.deps.Store.ChatTurnCounts(r.Context(), ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	out := make([]chatSearchHitBody, 0, len(hits))
	for i := range hits {
		h := &hits[i]
		body := renderChat(&h.Chat)
		n := counts[h.Chat.ID]
		body.TurnCount = &n
		hit := chatSearchHitBody{Chat: body, MatchCount: h.MatchCount, Matches: make([]chatSearchMatchBody, 0, len(h.Matches))}
		for _, m := range h.Matches {
			mb := chatSearchMatchBody{Seq: m.Seq, Field: m.Field, Segments: make([]chatSearchSegmentBody, 0, len(m.Snippet))}
			for _, p := range m.Snippet {
				mb.Snippet += p.Text
				mb.Segments = append(mb.Segments, chatSearchSegmentBody{Text: p.Text, Match: p.Match})
			}
			hit.Matches = append(hit.Matches, mb)
		}
		out = append(out, hit)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}
