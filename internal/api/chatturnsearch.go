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
// The text searched is every string a record says — the fields the TUI's
// recordSearchText joins (searchedFields) — not only what a verbosity level
// draws (decision 3), so a match inside a folded tool call is still found. A turn whose transcript is gone falls back to its
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

// searchedFields are the record keys whose strings a search counts, and
// searchedNested the keys of the objects inside a record's arrays. They are
// exactly what the TUI's recordSearchText (internal/tui/chatfind.go) joins
// for a loaded turn, so the daemon's count for an unloaded turn is the
// count the client arrives at once it loads it (task 160 decision 3) — the
// two lists change together. Metadata a reader never reads as the turn's
// text is deliberately left out: a run header's work_dir and
// available_tools, model usage, statuses, phases and the like would make
// `edit` or a worktree path match every turn (review F1).
var (
	searchedFields = []string{
		"text", "message", "summary", "line", "result_text", "output", "patch",
		"description", "error", "args", "name", "last_tool", "stop_reason",
	}
	searchedNested = map[string][]string{
		"tools":   {"name", "summary"},
		"results": {"name", "summary", "verb"},
		"items":   {"text"},
	}
)

// countRecord counts needle in the searched strings of one normalized record.
func countRecord(rec any, needle string) int {
	b, err := json.Marshal(rec)
	if err != nil {
		return 0
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return 0
	}
	n := 0
	for _, k := range searchedFields {
		n += countString(m[k], needle)
	}
	for k, keys := range searchedNested {
		elems, _ := m[k].([]any)
		for _, e := range elems {
			obj, _ := e.(map[string]any)
			for _, kk := range keys {
				n += countString(obj[kk], needle)
			}
		}
	}
	return n
}

func countString(v any, needle string) int {
	s, _ := v.(string)
	return countFold(s, needle)
}

// countFold counts non-overlapping case-insensitive occurrences of needle
// (already lower-cased) in s.
func countFold(s, needle string) int {
	if needle == "" || s == "" {
		return 0
	}
	return strings.Count(strings.ToLower(s), needle)
}
