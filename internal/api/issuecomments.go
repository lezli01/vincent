package api

import (
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/issues"
	"github.com/lezli01/vincent/internal/store"
)

// issueCommentBody is one comment of an issue's thread (task 130 decision
// 24, 130.16). Remote marks a comment mirrored from GitHub; there is no url,
// because the table stores none.
type issueCommentBody struct {
	ID        int64     `json:"id"`
	IssueID   int64     `json:"issue_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	Remote    bool      `json:"remote"`
	RemoteKey string    `json:"remote_key,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func renderIssueComment(c *store.IssueComment) issueCommentBody {
	return issueCommentBody{
		ID:        c.ID,
		IssueID:   c.IssueID,
		Author:    c.Author,
		Body:      c.Body,
		Remote:    c.RemoteKey != "",
		RemoteKey: c.RemoteKey,
		CreatedAt: c.CreatedAt.UTC(),
		UpdatedAt: c.UpdatedAt.UTC(),
	}
}

// handleIssueComments implements GET /v1/issues/{id}/comments: the thread,
// oldest first. The issue is read first so an unknown one is a 404 rather
// than an empty thread.
func (s *Server) handleIssueComments(w http.ResponseWriter, r *http.Request) {
	id, ok := issueIDFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, err := s.deps.Store.GetIssue(ctx, id); err != nil {
		s.writeIssueError(w, r, id, "get issue", err)
		return
	}
	list, err := issues.New(s.deps.Store).Comments(ctx, id)
	if err != nil {
		s.internalError(w, "list issue comments", err)
		return
	}
	out := make([]issueCommentBody, 0, len(list))
	for _, c := range list {
		out = append(out, renderIssueComment(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": out})
}

// issueCommentRequest is POST /v1/issues/{id}/comments' body. There is no
// author field: the daemon derives it as it does a create's (decision 24.6).
type issueCommentRequest struct {
	Body string `json:"body"`
}

// handleIssueCommentCreate implements POST /v1/issues/{id}/comments: a local
// comment. One on an issue with a live remote is the 409 issue_mirrored a
// title edit gets, refused before any I/O beyond reading the issue — nothing
// is ever posted to GitHub (task 130 decision 24, 130.16).
//
// It takes no Idempotency-Key, like the pull request comment route: a
// duplicate local comment is visible and harmless, and the key table
// (migration 0037) is scoped to issue create.
func (s *Server) handleIssueCommentCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := issueIDFromPath(w, r)
	if !ok {
		return
	}
	var req issueCommentRequest
	// The large tier, as an issue body's: a comment is prose that reaches a
	// task's prompt through the issue snapshot.
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	// §13.2's field bound, the one an issue body is held to.
	if msg := boundString("body", req.Body, maxDescriptionBytes); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	ctx := r.Context()
	author, _ := issueAuthor(ctx)
	c, err := issues.New(s.deps.Store).Comment(ctx, issueActor(r), id, author, req.Body)
	if err != nil {
		s.writeIssueError(w, r, id, "add issue comment", err)
		return
	}
	writeJSON(w, http.StatusCreated, renderIssueComment(c))
}
