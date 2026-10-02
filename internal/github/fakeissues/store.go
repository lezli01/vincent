package fakeissues

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Row is one corpus entry, kept as decoded JSON rather than a struct so a
// field the fake does not know about survives a write untouched. Numbers are
// json.Number, so an id is written back exactly as it was read.
type Row = map[string]any

// corpusRepo is the repository the built-in corpus is written against,
// cmd/fakegh's pull request corpus's too.
const corpusRepo = "octo/repo"

// builtin is the corpus the porcelain has always served (#200 and #41, both
// open), in REST shape. Two issues with different metadata coverage: one
// carrying labels, an assignee and a milestone, one carrying none, so a
// prefill test can assert both the filled and the empty mapping.
const builtin = `[
  {
    "id": 2000200,
    "node_id": "I_kwDOAAAAAc4AAAAAAAAAyA",
    "number": 200,
    "title": "GitHub integration: select a GitHub issue when creating a task",
    "body": "Most work in a GitHub-hosted repo starts life as a GitHub issue.",
    "state": "open",
    "state_reason": null,
    "closed_at": null,
    "created_at": "2026-08-26T19:21:29Z",
    "updated_at": "2026-08-26T19:30:00Z",
    "labels": [{"id": 1, "name": "enhancement"}, {"id": 2, "name": "area/api"}],
    "assignees": [{"login": "hubot"}],
    "user": {"login": "octocat"},
    "milestone": {"number": 4, "title": "v0.2.0"},
    "comments": 0,
    "url": "https://api.github.com/repos/octo/repo/issues/200",
    "html_url": "https://github.com/octo/repo/issues/200"
  },
  {
    "id": 2000041,
    "node_id": "I_kwDOAAAAAc4AAAAAAAAAKQ",
    "number": 41,
    "title": "Board header truncates on narrow terminals",
    "body": "",
    "state": "open",
    "state_reason": null,
    "closed_at": null,
    "created_at": "2026-07-01T08:00:00Z",
    "updated_at": "2026-07-02T08:00:00Z",
    "labels": [],
    "assignees": [],
    "user": {"login": "hubot"},
    "milestone": null,
    "comments": 0,
    "url": "https://api.github.com/repos/octo/repo/issues/41",
    "html_url": "https://github.com/octo/repo/issues/41"
  }
]`

// Store is one corpus. The zero value serves the built-in corpus and keeps
// no write.
type Store struct {
	// Path is FAKEGH_ISSUES_FILE. Empty, the built-in corpus answers and a
	// write is answered but not kept; set but missing, the built-in corpus
	// seeds it.
	Path string
	// Repo is FAKEGH_REPO (`owner/name`): every string naming octo/repo as a
	// path segment names it instead, the way cmd/fakegh rehomes its pull
	// requests.
	Repo string
	// Now stamps a write's updated_at and closed_at, and the rate limit's
	// reset; time.Now when nil.
	Now func() time.Time
}

// FromEnv is the Store cmd/fakegh serves: FAKEGH_ISSUES_FILE and
// FAKEGH_REPO.
func FromEnv() Store {
	return Store{Path: os.Getenv("FAKEGH_ISSUES_FILE"), Repo: os.Getenv("FAKEGH_REPO")}
}

// Scenario is the scenario for this invocation: the trimmed content of
// FAKEGH_SCENARIO_FILE when that names a non-empty file, FAKEGH_SCENARIO
// otherwise. The file is read on every call, which is what lets a gate flip
// one running daemon between `unreachable` and `success` without a restart.
func Scenario() string {
	if path := os.Getenv("FAKEGH_SCENARIO_FILE"); path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			if s := strings.TrimSpace(string(raw)); s != "" {
				return s
			}
		}
	}
	return os.Getenv("FAKEGH_SCENARIO")
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Load reads the corpus, seeding a missing file from the built-in one.
func (s Store) Load() ([]Row, error) {
	if s.Path == "" {
		return s.builtin()
	}
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		rows, err := s.builtin()
		if err != nil {
			return nil, err
		}
		return rows, s.Save(rows)
	}
	if err != nil {
		return nil, err
	}
	rows, err := decode(raw)
	if err != nil {
		return nil, fmt.Errorf("fakeissues: %s: %w", s.Path, err)
	}
	return s.rehome(rows), nil
}

func (s Store) builtin() ([]Row, error) {
	rows, err := decode([]byte(builtin))
	if err != nil {
		return nil, err
	}
	return s.rehome(rows), nil
}

func decode(raw []byte) ([]Row, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var rows []Row
	if err := dec.Decode(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Save writes the corpus back. It writes a temp file beside the corpus and
// renames it over, so a reader in another invocation sees the old corpus or
// the new one and never half of either. Windows refuses a rename over a file
// another process has open, and the daemon may be mid-read in a concurrent
// invocation, so the rename is retried briefly rather than failed.
func (s Store) Save(rows []Row) error {
	if s.Path == "" {
		return nil
	}
	encoded, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".fakegh-issues-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	for attempt := 0; ; attempt++ {
		err = os.Rename(tmp.Name(), s.Path)
		if err == nil || attempt == 40 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// rehome moves every string naming corpusRepo as a path segment into Repo.
// Unset, the rows are untouched, which is what every test and gate reads.
func (s Store) rehome(rows []Row) []Row {
	owner, name, ok := strings.Cut(s.Repo, "/")
	if !ok || owner == "" || name == "" {
		return rows
	}
	for _, row := range rows {
		rehomeValue(row, "/"+corpusRepo+"/", "/"+s.Repo+"/")
	}
	return rows
}

func rehomeValue(v any, from, to string) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, from, to)
	case map[string]any:
		for k, inner := range x {
			x[k] = rehomeValue(inner, from, to)
		}
	case []any:
		for i, inner := range x {
			x[i] = rehomeValue(inner, from, to)
		}
	}
	return v
}

// List answers `gh issue list --state S --limit N`: issue rows only — no pull
// request, comment or `_fake`-marked row — newest first, the order gh lists
// in. `--state` and `--limit` are honoured for the reason task 064 decision 9
// gives for `pr list`: a fake that answered every state with the open rows
// would let a test pass for the wrong reason.
func (s Store) List(state string, limit int) ([]map[string]any, error) {
	rows, err := s.Load()
	if err != nil {
		return nil, err
	}
	if state == "" {
		state = "open"
	}
	if limit <= 0 {
		limit = 30
	}
	issues := make([]Row, 0, len(rows))
	for _, row := range rows {
		if !isIssue(row) || isPull(row) || marked(row) {
			continue
		}
		if state != "all" && str(row, "state") != state {
			continue
		}
		issues = append(issues, row)
	}
	sortRows(issues, "created", false)
	if len(issues) > limit {
		issues = issues[:limit]
	}
	out := make([]map[string]any, 0, len(issues))
	for _, row := range issues {
		out = append(out, Porcelain(row))
	}
	return out, nil
}

// Issue answers `gh issue view N`, by the rules List lists by.
func (s Store) Issue(number int) (map[string]any, bool, error) {
	rows, err := s.Load()
	if err != nil {
		return nil, false, err
	}
	for _, row := range rows {
		if n, ok := intField(row, "number"); ok && n == number && isIssue(row) && !isPull(row) && !marked(row) {
			return Porcelain(row), true, nil
		}
	}
	return nil, false, nil
}

// Porcelain projects a REST row into `gh issue list --json`'s shape, which
// is how one stored form serves both surfaces: upper-case state, html_url as
// url, user as author, camelCase times, and labels and milestone cut to what
// gh reports.
func Porcelain(row Row) map[string]any {
	labels := []map[string]any{}
	for _, l := range list(row["labels"]) {
		if m, ok := l.(map[string]any); ok {
			labels = append(labels, map[string]any{"name": m["name"]})
		}
	}
	assignees := []map[string]any{}
	for _, a := range list(row["assignees"]) {
		if m, ok := a.(map[string]any); ok {
			assignees = append(assignees, map[string]any{"login": m["login"]})
		}
	}
	var milestone any
	if m, ok := row["milestone"].(map[string]any); ok {
		milestone = map[string]any{"number": m["number"], "title": m["title"]}
	}
	author := map[string]any{"login": ""}
	if u, ok := row["user"].(map[string]any); ok {
		author["login"] = u["login"]
	}
	number, _ := intField(row, "number")
	return map[string]any{
		"number":    number,
		"title":     str(row, "title"),
		"body":      str(row, "body"),
		"url":       str(row, "html_url"),
		"state":     strings.ToUpper(str(row, "state")),
		"labels":    labels,
		"author":    author,
		"assignees": assignees,
		"milestone": milestone,
		"createdAt": str(row, "created_at"),
		"updatedAt": str(row, "updated_at"),
	}
}

// isIssue tells an issue (or pull request) row from a comment row, which
// carries issue_url and no number.
func isIssue(row Row) bool {
	_, comment := row["issue_url"]
	return !comment
}

func isPull(row Row) bool { return row["pull_request"] != nil }

func fake(row Row) map[string]any {
	m, _ := row["_fake"].(map[string]any)
	return m
}

// marked reports a row carrying a fault marker, which no listing includes.
func marked(row Row) bool {
	f := fake(row)
	return f != nil && (f["deleted"] == true || f["transferred_to"] != nil)
}

// public is row without the fake-only key, which never appears in output.
func public(row Row) Row {
	if _, ok := row["_fake"]; !ok {
		return row
	}
	out := make(Row, len(row))
	for k, v := range row {
		if k != "_fake" {
			out[k] = v
		}
	}
	return out
}

func str(row Row, key string) string {
	s, _ := row[key].(string)
	return s
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func intField(row Row, key string) (int, bool) {
	switch v := row[key].(type) {
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

func timeField(row Row, key string) time.Time {
	t, _ := time.Parse(time.RFC3339, str(row, key))
	return t
}

// sortRows orders rows by GitHub's sort keys, ties broken by number (or id,
// for comments) in the same direction so a page boundary is stable.
func sortRows(rows []Row, by string, asc bool) {
	tie := func(r Row) int {
		if n, ok := intField(r, "number"); ok {
			return n
		}
		n, _ := intField(r, "id")
		return n
	}
	less := func(a, b Row) bool {
		switch by {
		case "updated":
			ta, tb := timeField(a, "updated_at"), timeField(b, "updated_at")
			if !ta.Equal(tb) {
				return ta.Before(tb)
			}
		case "comments":
			ca, _ := intField(a, "comments")
			cb, _ := intField(b, "comments")
			if ca != cb {
				return ca < cb
			}
		default:
			ta, tb := timeField(a, "created_at"), timeField(b, "created_at")
			if !ta.Equal(tb) {
				return ta.Before(tb)
			}
		}
		return tie(a) < tie(b)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if asc {
			return less(rows[i], rows[j])
		}
		return less(rows[j], rows[i])
	})
}
