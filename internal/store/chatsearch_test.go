package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatstate"
)

// searchChat creates a chat titled title with one finished turn per
// prompt/answer pair.
func searchChat(t *testing.T, s *Store, projectID int64, title string, turns ...[2]string) *Chat {
	t.Helper()
	ctx := t.Context()
	c := &Chat{
		ProjectID: projectID, Title: title, Agent: "claude",
		Branch: "vincent/x-" + strings.ReplaceAll(title, " ", "-"), BaseBranch: "main",
		PermissionMode: "full_auto",
	}
	if err := s.CreateChat(ctx, c); err != nil {
		t.Fatalf("CreateChat(%s): %v", title, err)
	}
	for _, tu := range turns {
		turn, err := s.CreateChatTurn(ctx, c.ID, tu[0])
		if err != nil {
			t.Fatalf("CreateChatTurn: %v", err)
		}
		turn.State = chatstate.TurnDone
		turn.ResultText = tu[1]
		if err := s.UpdateChatTurn(ctx, turn); err != nil {
			t.Fatalf("UpdateChatTurn: %v", err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE chats SET state = 'idle' WHERE id = ?`, c.ID); err != nil {
			t.Fatalf("idle: %v", err)
		}
	}
	return c
}

func setChatState(t *testing.T, s *Store, id int64, st chatstate.State) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), `UPDATE chats SET state = ? WHERE id = ?`, string(st), id); err != nil {
		t.Fatalf("set state: %v", err)
	}
}

func searchIDs(t *testing.T, s *Store, q string, opts ChatSearchOptions) []int64 {
	t.Helper()
	hits, err := s.SearchChats(t.Context(), q, opts)
	if err != nil {
		t.Fatalf("SearchChats(%q): %v", q, err)
	}
	ids := make([]int64, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.Chat.ID)
	}
	slices.Sort(ids)
	return ids
}

func snippetText(parts []SnippetPart) (all, matched string) {
	for _, p := range parts {
		all += p.Text
		if p.Match {
			matched += p.Text
		}
	}
	return all, matched
}

// TestFTS5IsCompiledIn fails loudly if a driver bump drops FTS5 or its
// trigram tokenizer, which chat search has no fallback for (task 158
// decision 1).
func TestFTS5IsCompiledIn(t *testing.T) {
	s := openTest(t)
	if _, err := s.db.ExecContext(t.Context(),
		`CREATE VIRTUAL TABLE temp.fts_probe USING fts5(x, tokenize = 'trigram')`); err != nil {
		t.Fatalf("the SQLite driver has no FTS5 trigram tokenizer: %v", err)
	}
}

// TestChatSearchFindsTitlePromptAndAnswer: each indexed field is found, a
// title match finds a chat with no turns, the snippet marks the match, and
// a substring inside an identifier matches.
func TestChatSearchFindsTitlePromptAndAnswer(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	empty := searchChat(t, s, p.ID, "flaky widget investigation")
	c := searchChat(t, s, p.ID, "talk",
		[2]string{"why does the handler fail", "it returns status429Body when throttled"},
		[2]string{"look at body_format please", "done"})

	hits, err := s.SearchChats(t.Context(), "widget", ChatSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Chat.ID != empty.ID || hits[0].Matches[0].Field != ChatSearchFieldTitle {
		t.Fatalf("title search = %+v, want the turnless chat by its title", hits)
	}

	hits, err = s.SearchChats(t.Context(), "429", ChatSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Chat.ID != c.ID {
		t.Fatalf("429 search = %+v, want chat %d", hits, c.ID)
	}
	m := hits[0].Matches[0]
	all, matched := snippetText(m.Snippet)
	if m.Seq != 1 || m.Field != ChatSearchFieldResult || matched != "429" || !strings.Contains(all, "status429Body") {
		t.Errorf("match = %+v (text %q, matched %q), want turn 1's answer with 429 marked", m, all, matched)
	}

	hits, err = s.SearchChats(t.Context(), "BODY_FORMAT", ChatSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Matches[0].Seq != 2 || hits[0].Matches[0].Field != ChatSearchFieldPrompt {
		t.Errorf("prompt search = %+v, want turn 2's prompt, case-insensitively", hits)
	}

	// Several terms AND; one in the prompt and one in the answer is a turn
	// matching in both fields.
	hits, err = s.SearchChats(t.Context(), "handler throttled", ChatSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].MatchCount != 2 {
		t.Errorf("two-field search = %+v, want one chat matched in both fields of turn 1", hits)
	}
	if ids := searchIDs(t, s, "handler widget", ChatSearchOptions{}); len(ids) != 0 {
		t.Errorf("terms in different chats matched %v, want none: terms AND", ids)
	}
}

// TestChatSearchStaysInSync: a new turn, a finished answer and a rename are
// findable at once, and deleting a chat or its project leaves no hit.
func TestChatSearchStaysInSync(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	c := searchChat(t, s, p.ID, "original title")

	turn, err := s.CreateChatTurn(ctx, c.ID, "a prompt about kumquats")
	if err != nil {
		t.Fatal(err)
	}
	if ids := searchIDs(t, s, "kumquat", ChatSearchOptions{}); !slices.Equal(ids, []int64{c.ID}) {
		t.Errorf("after insert = %v, want the prompt found", ids)
	}
	if ids := searchIDs(t, s, "persimmon", ChatSearchOptions{}); len(ids) != 0 {
		t.Errorf("before the answer = %v, want nothing", ids)
	}
	turn.State = chatstate.TurnDone
	turn.ResultText = "the answer is persimmon"
	if err := s.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if ids := searchIDs(t, s, "persimmon", ChatSearchOptions{}); !slices.Equal(ids, []int64{c.ID}) {
		t.Errorf("after finishing = %v, want the answer found", ids)
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE chats SET title = 'renamed tangerine', state = 'idle' WHERE id = ?`, c.ID); err != nil {
		t.Fatal(err)
	}
	if ids := searchIDs(t, s, "tangerine", ChatSearchOptions{}); !slices.Equal(ids, []int64{c.ID}) {
		t.Errorf("after rename = %v, want the new title found", ids)
	}
	if ids := searchIDs(t, s, "original", ChatSearchOptions{}); len(ids) != 0 {
		t.Errorf("after rename the old title still matches %v", ids)
	}

	setChatState(t, s, c.ID, chatstate.Archived)
	if err := s.DeleteChatCascade(ctx, c.ID); err != nil {
		t.Fatalf("DeleteChatCascade: %v", err)
	}
	for _, q := range []string{"kumquat", "persimmon", "tangerine"} {
		if ids := searchIDs(t, s, q, ChatSearchOptions{Archived: ArchivedAll}); len(ids) != 0 {
			t.Errorf("after delete %q = %v, want nothing", q, ids)
		}
	}
	assertFTSEmpty(t, s)

	p2 := testProject(t, s, "p2")
	searchChat(t, s, p2.ID, "project bound mango", [2]string{"mango prompt", "mango answer"})
	if err := s.DeleteProjectCascade(ctx, p2.ID); err != nil {
		t.Fatalf("DeleteProjectCascade: %v", err)
	}
	if ids := searchIDs(t, s, "mango", ChatSearchOptions{Archived: ArchivedAll}); len(ids) != 0 {
		t.Errorf("after project delete = %v, want nothing", ids)
	}
	assertFTSEmpty(t, s)
}

// assertFTSEmpty checks the indexes themselves, not just the joined query:
// an orphaned index row would be invisible through the join.
func assertFTSEmpty(t *testing.T, s *Store) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO chat_title_fts (chat_title_fts) VALUES ('integrity-check')`,
		`INSERT INTO chat_turn_fts (chat_turn_fts) VALUES ('integrity-check')`,
	} {
		if _, err := s.db.ExecContext(t.Context(), q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
}

// TestChatSearchScope: the archived window splits chats exactly as
// ListChats does, and project_id filters.
func TestChatSearchScope(t *testing.T) {
	s := openTest(t)
	p1 := testProject(t, s, "p1")
	p2 := testProject(t, s, "p2")
	live := searchChat(t, s, p1.ID, "live needle")
	byState := map[chatstate.State]int64{}
	for _, st := range []chatstate.State{chatstate.Archived, chatstate.HandedOff, chatstate.Closed} {
		c := searchChat(t, s, p1.ID, string(st)+" needle")
		setChatState(t, s, c.ID, st)
		byState[st] = c.ID
	}
	other := searchChat(t, s, p2.ID, "other needle")

	all := []int64{live.ID, byState[chatstate.Archived], byState[chatstate.HandedOff], byState[chatstate.Closed], other.ID}
	slices.Sort(all)
	if got := searchIDs(t, s, "needle", ChatSearchOptions{Archived: ArchivedAll}); !slices.Equal(got, all) {
		t.Errorf("all = %v, want %v", got, all)
	}
	for _, mode := range []ArchivedFilter{ArchivedExclude, ArchivedOnly} {
		listed, err := s.ListChats(t.Context(), ChatFilter{Archived: mode})
		if err != nil {
			t.Fatal(err)
		}
		var want []int64
		for _, c := range listed {
			want = append(want, c.ID)
		}
		slices.Sort(want)
		if got := searchIDs(t, s, "needle", ChatSearchOptions{Archived: mode}); !slices.Equal(got, want) {
			t.Errorf("archived mode %v = %v, want ListChats' %v", mode, got, want)
		}
	}
	if got := searchIDs(t, s, "needle", ChatSearchOptions{ProjectID: &p2.ID, Archived: ArchivedAll}); !slices.Equal(got, []int64{other.ID}) {
		t.Errorf("project filter = %v, want [%d]", got, other.ID)
	}
}

// TestChatSearchQuerySafety: FTS5 syntax in the text is only ever text, and
// short or empty queries are refused with typed errors.
func TestChatSearchQuerySafety(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	c := searchChat(t, s, p.ID, `quote "this" NEAR(stuff) -flag AND star*`)
	for _, q := range []string{`"this"`, `star*`, `-flag`, `NEAR(stuff)`, `AND`, `(((`, `"""`, `)*-"`, `NOT NEAR`, `col:umn`, `^start`} {
		hits, err := s.SearchChats(t.Context(), q, ChatSearchOptions{})
		if err != nil {
			t.Errorf("SearchChats(%q) = %v, want results or nothing", q, err)
		}
		_ = hits
	}
	for _, q := range []string{`"this"`, `star*`, `-flag`, `NEAR(stuff)`, `AND`} {
		if ids := searchIDs(t, s, q, ChatSearchOptions{}); !slices.Equal(ids, []int64{c.ID}) {
			t.Errorf("%q matched %v, want the chat whose title holds it literally", q, ids)
		}
	}
	for q, want := range map[string]error{"": ErrSearchQueryEmpty, "   ": ErrSearchQueryEmpty, "ab": ErrSearchQueryTooShort, "abc de": ErrSearchQueryTooShort} {
		if _, err := s.SearchChats(t.Context(), q, ChatSearchOptions{}); !errors.Is(err, want) {
			t.Errorf("SearchChats(%q) err = %v, want %v", q, err, want)
		}
	}
}

// TestChatSearchCaps: the per-chat cap keeps the first matches in
// conversation order and reports the full count.
func TestChatSearchCaps(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	var turns [][2]string
	for range 8 {
		turns = append(turns, [2]string{"say walrus", "ok"})
	}
	searchChat(t, s, p.ID, "c1", turns...)
	searchChat(t, s, p.ID, "c2", [2]string{"walrus", "ok"})
	hits, err := s.SearchChats(t.Context(), "walrus", ChatSearchOptions{Limit: 1, TurnsPerChat: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("limit 1 gave %d chats", len(hits))
	}
	if len(hits) == 1 && hits[0].Chat.Title == "c1" {
		h := hits[0]
		if len(h.Matches) != 3 || h.MatchCount != 8 || h.Matches[0].Seq != 1 || h.Matches[2].Seq != 3 {
			t.Errorf("c1 hit = %d matches (count %d), first seq %d, want 3 of 8 from seq 1", len(h.Matches), h.MatchCount, h.Matches[0].Seq)
		}
	}
}

// TestChatSearchSurvivesRetention: transcript retention deletes files, not
// rows, so an answer stays findable after its transcript is gone. The
// store holds no transcript at all — this pins that the index reads only
// the row.
func TestChatSearchSurvivesRetention(t *testing.T) {
	s := openTest(t)
	p := testProject(t, s, "p1")
	c := searchChat(t, s, p.ID, "old talk", [2]string{"prompt", "a retained answer about quinces"})
	setChatState(t, s, c.ID, chatstate.Archived)
	if ids := searchIDs(t, s, "quinces", ChatSearchOptions{Archived: ArchivedAll}); !slices.Equal(ids, []int64{c.ID}) {
		t.Errorf("retained answer = %v, want found", ids)
	}
}

// TestMigration0061BackfillsChatSearch: a database at 0060 holding chats
// with turns gains the index on open, and the backfilled rows are found.
func TestMigration0061BackfillsChatSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 60)
	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		for _, q := range []string{
			`INSERT INTO projects (id, name, path, default_branch, created_at, updated_at)
				VALUES (1, 'p1', '/p1', 'main', '` + backfillAt(0) + `', '` + backfillAt(0) + `')`,
			`INSERT INTO chats (id, project_id, title, state, agent, branch, base_branch, created_at, updated_at)
				VALUES (1, 1, 'backfilled apricot', 'archived', 'claude', 'b1', 'main', '` + backfillAt(1) + `', '` + backfillAt(1) + `')`,
			`INSERT INTO chat_turns (chat_id, seq, prompt, state, result_text, started_at)
				VALUES (1, 1, 'a prompt naming loquat', 'done', 'an answer naming feijoa', '` + backfillAt(2) + `')`,
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating 60 -> 61): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, q := range []string{"apricot", "loquat", "feijoa"} {
		if ids := searchIDs(t, s, q, ChatSearchOptions{Archived: ArchivedAll}); !slices.Equal(ids, []int64{1}) {
			t.Errorf("backfilled %q = %v, want [1]", q, ids)
		}
	}
}
