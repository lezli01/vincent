package fakeissues

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The comment helpers are how a test edits the corpus's thread without
// hand-writing a REST comment row (task 130 decision 24, 130.16). Each one
// loads, edits and saves the file, so it reaches a daemon that reads the
// same FAKEGH_ISSUES_FILE on its next call. None of them touches the issue
// row: its `comments` count follows from the comment rows (countComments),
// and its updated_at stays put, so an edit to a comment changes the
// comment listing's ETag and never the issue listing's — the two
// conditional listings a sync revalidates move independently. Only an added
// or removed comment changes the issue listing, through the count.

// ErrNoCorpusFile is a comment helper's answer on a Store without a Path:
// the built-in corpus keeps no write, and a helper that succeeded without
// one would let a test assert against a change nothing can see.
var ErrNoCorpusFile = errors.New("fakeissues: the comment helpers need a corpus file (Store.Path)")

// AddComment files a comment by author under issue number — an issue or a
// pull request row, as GitHub's issue comments include a pull request's
// conversation comments — created and updated now, and returns its id: one
// more than the largest id in the corpus, so it collides with no row.
func (s Store) AddComment(number int, author, body string) (int64, error) {
	if s.Path == "" {
		return 0, ErrNoCorpusFile
	}
	rows, err := s.Load()
	if err != nil {
		return 0, err
	}
	if _, ok := find(rows, number); !ok {
		return 0, fmt.Errorf("fakeissues: no issue or pull request #%d in the corpus", number)
	}
	var id int64
	for _, row := range rows {
		if n, ok := intField(row, "id"); ok && int64(n) > id {
			id = int64(n)
		}
	}
	id++
	repo := s.Repo
	if repo == "" {
		repo = corpusRepo
	}
	sid := strconv.FormatInt(id, 10)
	stamp := s.now().Format(time.RFC3339)
	rows = append(rows, Row{
		"id":         json.Number(sid),
		"node_id":    "IC_fake" + sid,
		"url":        apiBase + "repos/" + repo + "/issues/comments/" + sid,
		"html_url":   fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-%s", repo, number, sid),
		"issue_url":  fmt.Sprintf("%srepos/%s/issues/%d", apiBase, repo, number),
		"body":       body,
		"user":       map[string]any{"login": author},
		"created_at": stamp,
		"updated_at": stamp,
	})
	return id, s.Save(rows)
}

// EditComment replaces comment id's body and moves its updated_at forward —
// strictly, as patchIssue does, so two edits inside one second still reach
// a `since` walk and change the listing.
func (s Store) EditComment(id int64, body string) error {
	if s.Path == "" {
		return ErrNoCorpusFile
	}
	rows, err := s.Load()
	if err != nil {
		return err
	}
	i, ok := findComment(rows, id)
	if !ok {
		return fmt.Errorf("fakeissues: no comment %d in the corpus", id)
	}
	row := rows[i]
	row["body"] = body
	now := s.now()
	if prev := timeField(row, "updated_at"); !now.After(prev) {
		now = prev.Add(time.Second)
	}
	row["updated_at"] = now.Format(time.RFC3339)
	return s.Save(rows)
}

// RemoveComment deletes comment id from the corpus, as deleting it on
// GitHub does: it leaves both listings, and its issue's count drops.
func (s Store) RemoveComment(id int64) error {
	if s.Path == "" {
		return ErrNoCorpusFile
	}
	rows, err := s.Load()
	if err != nil {
		return err
	}
	i, ok := findComment(rows, id)
	if !ok {
		return fmt.Errorf("fakeissues: no comment %d in the corpus", id)
	}
	return s.Save(append(rows[:i], rows[i+1:]...))
}

func findComment(rows []Row, id int64) (int, bool) {
	for i, row := range rows {
		if n, ok := intField(row, "id"); ok && int64(n) == id && !isIssue(row) {
			return i, true
		}
	}
	return 0, false
}

// issueComments answers `repos/{o}/{r}/issues/{n}/comments`: the comment
// rows filed under n, oldest first — the endpoint takes no sort, and GitHub
// answers it in creation order — paged by per_page and page like every
// listing here. An unknown number is a 404 and a deleted issue a 410, as for
// the issue itself. A transferred one is a 301 to the target's thread unless
// the request follows redirects, when it answers the thread the issue took
// with it: the rows still filed under its old number here.
func (s Store) issueComments(rows []Row, n int, u *url.URL, follow bool) Response {
	row, ok := find(rows, n)
	if !ok {
		return s.errorResponse(http.StatusNotFound, "Not Found", docsComments+"#list-issue-comments")
	}
	if resp, ok := s.fault(row, false); ok {
		switch {
		case resp.Status != http.StatusMovedPermanently:
			return resp
		case !follow:
			resp.Header.Set("Location", resp.Header.Get("Location")+"/comments")
			return resp
		}
	}
	matched := make([]Row, 0)
	for _, r := range rows {
		if !isIssue(r) && commentIssue(r) == n {
			matched = append(matched, r)
		}
	}
	sortRows(matched, "created", true)
	return s.page(matched, u)
}
