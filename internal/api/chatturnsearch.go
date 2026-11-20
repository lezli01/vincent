package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// Bounds on GET /v1/chats/{id}/search (task 160 decision 2, §13.2).
const (
	// maxChatSearchQuery is the longest query, in bytes. A search line is
	// typed by a human; anything longer is a pasted blob, not a search.
	maxChatSearchQuery = 256
	// maxChatSearchScan is how much of one turn's transcript a search reads.
	// A turn past it is reported with `truncated: true` and the count of
	// what was read — the same answer a client gets from a ranged transcript
	// read that stops short, rather than an unbounded scan.
	maxChatSearchScan = 32 << 20
)

// chatSearchTurnDTO is one matching turn: its seq, how many times the query
// occurs in it, and where those were counted. No rendered text rides here —
// the client fetches the turn through the transcript route and finds the
// matches in its own lines (task 160 decision 2).
type chatSearchTurnDTO struct {
	Seq     int `json:"seq"`
	Matches int `json:"matches"`
	// Source is `transcript`, or `result_text` when the transcript file has
	// gone to retention (§17) and only the turn row was searched.
	Source    string `json:"source"`
	Truncated bool   `json:"truncated,omitempty"`
}

type chatSearchDTO struct {
	Query string              `json:"query"`
	Total int                 `json:"total"`
	Turns []chatSearchTurnDTO `json:"turns"`
}

// handleChatTurnSearch is GET /v1/chats/{id}/search?q= (task 160 decision 2):
// a case-insensitive plain-substring scan of every turn's prompt and
// normalized transcript text, the server half of the chat view's search.
// The text searched is every string a normalized record carries, not only
// what a verbosity level draws (decision 3), so a match inside a folded tool
// call is still found. A turn whose transcript is gone falls back to its
// row's result_text. It is not GET /v1/chats/search (task 158,
// handleChatSearch), which finds chats across the store through its own
// index; this one counts matches inside one chat's turns.
func (s *Server) handleChatTurnSearch(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	q := r.URL.Query().Get("q")
	switch {
	case strings.TrimSpace(q) == "":
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "q must not be empty")
		return
	case len(q) > maxChatSearchQuery:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("q must be at most %d bytes", maxChatSearchQuery))
		return
	case !utf8.ValidString(q):
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "q must be valid UTF-8")
		return
	}
	turns, err := s.deps.Store.ListChatTurns(r.Context(), chat.ID)
	if err != nil {
		s.internalError(w, "list chat turns", err)
		return
	}
	out := chatSearchDTO{Query: q, Turns: []chatSearchTurnDTO{}}
	needle := strings.ToLower(q)
	parse := s.transcriptParser(chat.Agent)
	for i := range turns {
		if r.Context().Err() != nil {
			return
		}
		hit, err := s.searchChatTurn(chat, &turns[i], needle, parse)
		if err != nil {
			s.internalError(w, "search chat turn", err)
			return
		}
		if hit.Matches > 0 {
			out.Turns = append(out.Turns, hit)
			out.Total += hit.Matches
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// searchChatTurn counts needle (already lower-cased) in one turn.
func (s *Server) searchChatTurn(
	chat *store.Chat, turn *store.ChatTurn, needle string, parse agent.LineParser,
) (chatSearchTurnDTO, error) {
	hit := chatSearchTurnDTO{Seq: turn.Seq, Source: "transcript"}
	hit.Matches = countFold(turn.Prompt, needle)
	path := filepath.Join(
		s.deps.Dirs.Data, "transcripts", worktree.ChatOwner(chat.ID).Dir(),
		fmt.Sprintf("%d.jsonl", turn.Seq))
	f, err := os.Open(path) //nolint:gosec // the path is built from two ids
	if err != nil {
		hit.Source = "result_text"
		hit.Matches += countFold(turn.ResultText, needle)
		return hit, nil
	}
	defer func() { _ = f.Close() }()
	n, truncated, err := searchTranscript(io.LimitReader(f, maxChatSearchScan+1), needle, parse)
	hit.Matches += n
	hit.Truncated = truncated
	return hit, err
}

// searchTranscript counts needle across the strings of every normalized
// record in src. A source longer than maxChatSearchScan reports truncated;
// its last, partial line is not counted.
func searchTranscript(src io.Reader, needle string, parse agent.LineParser) (int, bool, error) {
	br := bufio.NewReader(src)
	var read int64
	n := 0
	for {
		line, err := br.ReadBytes('\n')
		read += int64(len(line))
		if read > maxChatSearchScan {
			return n, true, nil
		}
		if trimmed := trimLine(line); len(trimmed) > 0 {
			for _, rec := range normalizeLine(trimmed, parse) {
				n += countRecord(rec, needle)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, false, nil
			}
			return n, false, err
		}
	}
}

// countRecord counts needle in every string value of one normalized record.
// It walks the record's JSON rather than naming fields, so a field a later
// record type adds is searched without this function learning about it. The
// keys that identify rather than say something — the record's type and the
// call ids — are skipped: searching "agent" must not hit every record.
func countRecord(rec any, needle string) int {
	b, err := json.Marshal(rec)
	if err != nil {
		return 0
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return 0
	}
	return countValue(v, "", needle)
}

func countValue(v any, key, needle string) int {
	switch t := v.(type) {
	case string:
		switch key {
		case "type", "call_id", "parent_call_id", "plan_call_id":
			return 0
		}
		return countFold(t, needle)
	case []any:
		n := 0
		for _, e := range t {
			n += countValue(e, key, needle)
		}
		return n
	case map[string]any:
		n := 0
		for k, e := range t {
			n += countValue(e, k, needle)
		}
		return n
	}
	return 0
}

// countFold counts non-overlapping case-insensitive occurrences of needle
// (already lower-cased) in s.
func countFold(s, needle string) int {
	if needle == "" || s == "" {
		return 0
	}
	return strings.Count(strings.ToLower(s), needle)
}
