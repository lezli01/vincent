package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Chat search (task 158, #1022, spec §5.5, §14): a full-text query over the
// two FTS5 indexes 0061_chat_search.sql keeps — chat titles and each turn's
// prompt and final answer.

// MinSearchTermRunes is the shortest term the trigram index can answer. A
// shorter one would need a full scan, which the index exists to avoid, so it
// is refused rather than served slowly (task 158 decision 2).
const MinSearchTermRunes = 3

// The defaults and ceilings for a search's caps. Conservative: the result is
// read by a human, grouped by chat, and a query that matches everything is
// one to narrow rather than page through.
const (
	DefaultChatSearchLimit = 20
	MaxChatSearchLimit     = 100
	DefaultChatSearchTurns = 5
	MaxChatSearchTurns     = 50
	// chatSearchRowCap bounds the index rows one search reads before
	// grouping. The best-ranked rows come first, so the cap only ever drops
	// the tail of a very broad query.
	chatSearchRowCap = 2000
)

// ErrSearchQueryEmpty and ErrSearchQueryTooShort are the two ways a search
// text is refused before it reaches SQLite.
var (
	ErrSearchQueryEmpty    = errors.New("search query is empty")
	ErrSearchQueryTooShort = fmt.Errorf("every search term must be at least %d characters", MinSearchTermRunes)
)

// Fields a search match can be in.
const (
	ChatSearchFieldTitle  = "title"
	ChatSearchFieldPrompt = "prompt"
	ChatSearchFieldResult = "result"
)

// The sentinels snippet() wraps a match in. They are control characters no
// title, prompt or answer the snippet is read from is expected to carry, and
// they never leave the store: parseSnippet turns them into SnippetParts, so
// the marking is data and no client parses markup or ANSI from the server.
const (
	snippetOpen  = "\x02"
	snippetClose = "\x03"
)

// SnippetPart is one run of a snippet's text; Match marks a run that matched
// the query.
type SnippetPart struct {
	Text  string
	Match bool
}

// ChatSearchMatch is one place a chat matched: its title (Seq 0) or one
// field of one turn.
type ChatSearchMatch struct {
	Seq     int
	Field   string
	Snippet []SnippetPart
}

// ChatSearchHit is one chat a search found, with the places it matched in
// conversation order (title first) and capped at the per-chat limit;
// MatchCount is how many there were before the cap.
type ChatSearchHit struct {
	Chat       Chat
	Matches    []ChatSearchMatch
	MatchCount int
}

// ChatSearchOptions scopes a search. Archived means what ChatFilter.Archived
// does — its zero value excludes every terminal chat — and the API is the
// party that defaults it to ArchivedAll for search (task 158 decision 3).
type ChatSearchOptions struct {
	ProjectID *int64
	Archived  ArchivedFilter
	// Limit caps the chats returned and TurnsPerChat the matches listed for
	// each; zero means the default, and both are clamped to their maximum.
	Limit        int
	TurnsPerChat int
}

// ChatSearchExpression turns a human's search text into a safe FTS5 MATCH
// expression: each whitespace-separated term is a double-quoted string with
// its embedded quotes doubled, and the terms are ANDed. FTS5 syntax is never
// passed through, so `"`, `*`, `-`, `NEAR`, `AND` or a parenthesis is only
// ever text to find.
func ChatSearchExpression(q string) (string, error) {
	terms := strings.Fields(q)
	if len(terms) == 0 {
		return "", ErrSearchQueryEmpty
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		if utf8.RuneCountInString(t) < MinSearchTermRunes {
			return "", ErrSearchQueryTooShort
		}
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " AND "), nil
}

type chatSearchRow struct {
	chatID int64
	seq    int
	field  string
	rank   float64
	parts  []SnippetPart
}

// SearchChats runs q over every chat's title, prompts and answers within
// opts' scope, and returns the chats it matched best first (bm25), each with
// its matches.
func (s *Store) SearchChats(ctx context.Context, q string, opts ChatSearchOptions) ([]ChatSearchHit, error) {
	expr, err := ChatSearchExpression(q)
	if err != nil {
		return nil, err
	}
	limit := clampSearch(opts.Limit, DefaultChatSearchLimit, MaxChatSearchLimit)
	perChat := clampSearch(opts.TurnsPerChat, DefaultChatSearchTurns, MaxChatSearchTurns)

	scope, scopeArgs := chatSearchScope(opts)
	snip := func(table string, col int) string {
		return fmt.Sprintf(`COALESCE(snippet(%s, %d, '%s', '%s', '…', 64), '')`, table, col, snippetOpen, snippetClose)
	}
	var rows []chatSearchRow

	titleQ := `SELECT c.id, bm25(chat_title_fts), ` + snip("chat_title_fts", 0) + `
		FROM chat_title_fts JOIN chats c ON c.id = chat_title_fts.rowid
		WHERE chat_title_fts MATCH ?` + scope + `
		ORDER BY bm25(chat_title_fts) LIMIT ?`
	if err := s.searchRows(ctx, titleQ, append(append([]any{expr}, scopeArgs...), chatSearchRowCap),
		func(sc func(...any) error) error {
			var r chatSearchRow
			var text string
			if err := sc(&r.chatID, &r.rank, &text); err != nil {
				return err
			}
			r.field = ChatSearchFieldTitle
			r.parts = parseSnippet(text)
			rows = append(rows, r)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("search chat titles: %w", err)
	}

	turnQ := `SELECT t.chat_id, t.seq, bm25(chat_turn_fts), ` + snip("chat_turn_fts", 0) + `, ` + snip("chat_turn_fts", 1) + `
		FROM chat_turn_fts JOIN chat_turns t ON t.id = chat_turn_fts.rowid JOIN chats c ON c.id = t.chat_id
		WHERE chat_turn_fts MATCH ?` + scope + `
		ORDER BY bm25(chat_turn_fts) LIMIT ?`
	if err := s.searchRows(ctx, turnQ, append(append([]any{expr}, scopeArgs...), chatSearchRowCap),
		func(sc func(...any) error) error {
			var (
				chatID       int64
				seq          int
				rank         float64
				prompt, rslt string
			)
			if err := sc(&chatID, &seq, &rank, &prompt, &rslt); err != nil {
				return err
			}
			// A turn can match in either field, or — with several terms —
			// partly in each; every field that carries a highlight is a
			// match of its own, so the reader sees where each term was.
			added := false
			for _, f := range []struct{ name, text string }{
				{ChatSearchFieldPrompt, prompt}, {ChatSearchFieldResult, rslt},
			} {
				if !strings.Contains(f.text, snippetOpen) {
					continue
				}
				rows = append(rows, chatSearchRow{chatID: chatID, seq: seq, field: f.name, rank: rank, parts: parseSnippet(f.text)})
				added = true
			}
			if !added {
				rows = append(rows, chatSearchRow{chatID: chatID, seq: seq, field: ChatSearchFieldPrompt, rank: rank, parts: parseSnippet(prompt)})
			}
			return nil
		}); err != nil {
		return nil, fmt.Errorf("search chat turns: %w", err)
	}

	// Group by chat. A chat ranks by its best row; ties go to the newer chat.
	type group struct {
		best float64
		rows []chatSearchRow
	}
	groups := map[int64]*group{}
	var order []int64
	for _, r := range rows {
		g, ok := groups[r.chatID]
		if !ok {
			g = &group{best: r.rank}
			groups[r.chatID] = g
			order = append(order, r.chatID)
		}
		g.best = min(g.best, r.rank)
		g.rows = append(g.rows, r)
	}
	sort.SliceStable(order, func(i, j int) bool {
		gi, gj := groups[order[i]], groups[order[j]]
		if gi.best != gj.best {
			return gi.best < gj.best
		}
		return order[i] > order[j]
	})
	if len(order) > limit {
		order = order[:limit]
	}
	if len(order) == 0 {
		return []ChatSearchHit{}, nil
	}
	chats, err := s.chatsByID(ctx, order)
	if err != nil {
		return nil, err
	}
	out := make([]ChatSearchHit, 0, len(order))
	for _, id := range order {
		c, ok := chats[id]
		if !ok {
			continue // deleted between the two reads
		}
		g := groups[id]
		// Conversation order inside a chat: the title, then by turn, the
		// prompt before its answer.
		sort.SliceStable(g.rows, func(i, j int) bool {
			if g.rows[i].seq != g.rows[j].seq {
				return g.rows[i].seq < g.rows[j].seq
			}
			return fieldOrder(g.rows[i].field) < fieldOrder(g.rows[j].field)
		})
		hit := ChatSearchHit{Chat: c, MatchCount: len(g.rows)}
		for _, r := range g.rows {
			if len(hit.Matches) == perChat {
				break
			}
			hit.Matches = append(hit.Matches, ChatSearchMatch{Seq: r.seq, Field: r.field, Snippet: r.parts})
		}
		out = append(out, hit)
	}
	return out, nil
}

func fieldOrder(f string) int {
	switch f {
	case ChatSearchFieldTitle:
		return 0
	case ChatSearchFieldPrompt:
		return 1
	default:
		return 2
	}
}

func clampSearch(n, def, maxN int) int {
	if n <= 0 {
		return def
	}
	return min(n, maxN)
}

// chatSearchScope is the WHERE tail both search queries share, over the
// joined `chats c`: the project and the archived window, with the same state
// set ListChats uses.
func chatSearchScope(opts ChatSearchOptions) (string, []any) {
	var q string
	var args []any
	if opts.ProjectID != nil {
		q += ` AND c.project_id = ?`
		args = append(args, *opts.ProjectID)
	}
	switch opts.Archived {
	case ArchivedExclude:
		q += ` AND c.state NOT IN (?, ?, ?)`
		args = append(args, terminalChatStateArgs()...)
	case ArchivedOnly:
		q += ` AND c.state IN (?, ?, ?)`
		args = append(args, terminalChatStateArgs()...)
	case ArchivedAll:
	}
	return q, args
}

func (s *Store) searchRows(ctx context.Context, q string, args []any, each func(func(...any) error) error) error {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := each(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) chatsByID(ctx context.Context, ids []int64) (map[int64]Chat, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	//nolint:gosec // G202: placeholders renders bind markers only, never values.
	rows, err := s.db.QueryContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id IN `+placeholders(len(ids)), args...)
	if err != nil {
		return nil, fmt.Errorf("search chats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]Chat, len(ids))
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		out[c.ID] = *c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chats: %w", err)
	}
	return out, nil
}

// parseSnippet splits snippet() output on its sentinels. Whitespace runs are
// folded to single spaces: a snippet is one line in every client, and a
// newline in the middle of one would break a row.
func parseSnippet(s string) []SnippetPart {
	var out []SnippetPart
	match := false
	for s != "" {
		sep := snippetOpen
		if match {
			sep = snippetClose
		}
		i := strings.Index(s, sep)
		chunk := s
		if i >= 0 {
			chunk = s[:i]
		}
		if chunk != "" {
			out = append(out, SnippetPart{Text: foldSpace(chunk), Match: match})
		}
		if i < 0 {
			break
		}
		s = s[i+len(sep):]
		match = !match
	}
	return out
}

func foldSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}
