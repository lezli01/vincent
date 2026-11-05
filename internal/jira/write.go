package jira

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/lezli01/vincent/internal/jira/jiramap"
)

// The write half (task 142.13, issue #909): the three calls the outbox's
// Jira source makes. Each is one request and never retries, like every
// other call; a 403 on a write is ReasonNoWriteScope rather than the read
// path's ReasonForbidden (task 142.13 decision 3), so the drain fails the
// row terminally the way it fails GitHub's missing write scope.

// transitionsPage is GET issue/{id}/transitions' answer.
type transitionsPage struct {
	Transitions []jiramap.Transition `json:"transitions"`
}

// Transitions lists the transitions available on the issue now, with each
// screen's fields (expand=transitions.fields), decoded straight into the
// shape jiramap.ChooseTransition reads.
func (c *Client) Transitions(ctx context.Context, idOrKey string) ([]jiramap.Transition, error) {
	q := url.Values{}
	q.Set("expand", expandTransFields)
	var page transitionsPage
	if err := c.do(ctx, "transitions", http.MethodGet, pathTransitions(idOrKey), q, nil, &page); err != nil {
		return nil, err
	}
	return page.Transitions, nil
}

// transitionRequest is POST issue/{id}/transitions' body. Fields is omitted
// entirely when nil: a screen without a resolution field rejects one.
type transitionRequest struct {
	Transition struct {
		ID string `json:"id"`
	} `json:"transition"`
	Fields *transitionFields `json:"fields,omitempty"`
}

type transitionFields struct {
	Resolution jiramap.Named `json:"resolution"`
}

// DoTransition performs one transition, setting resolution on its screen
// when it is not "" (and sending no `fields` key at all when it is). Jira
// answers 204; a 400 — a screen field it refused — is ReasonBadRequest.
func (c *Client) DoTransition(ctx context.Context, idOrKey, transitionID, resolution string) error {
	if transitionID == "" {
		return &Error{Op: "transition", Reason: ReasonBadRequest, Err: errors.New("no transition id")}
	}
	var body transitionRequest
	body.Transition.ID = transitionID
	if resolution != "" {
		body.Fields = &transitionFields{Resolution: jiramap.Named{Name: resolution}}
	}
	return writeScope(c.do(ctx, "transition", http.MethodPost, pathTransitions(idOrKey), nil, body, nil))
}

// commentRequest is POST issue/{id}/comment's body: an ADF document.
type commentRequest struct {
	Body json.RawMessage `json:"body"`
}

// AddComment posts adf, an Atlassian Document Format document, as a comment
// and returns Jira's id for it — the one Comments reports it under.
func (c *Client) AddComment(ctx context.Context, idOrKey string, adf json.RawMessage) (string, error) {
	if len(adf) == 0 {
		return "", &Error{Op: "add comment", Reason: ReasonBadRequest, Err: errors.New("empty comment body")}
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, "add comment", http.MethodPost, pathComments(idOrKey), nil, commentRequest{Body: adf}, &out); err != nil {
		return "", writeScope(err)
	}
	if out.ID == "" {
		return "", &Error{Op: "add comment", Status: http.StatusCreated, Reason: ReasonBadResponse, Err: errors.New("the answer carries no comment id")}
	}
	return out.ID, nil
}

// writeScope re-labels a write call's 403 as ReasonNoWriteScope. A CAPTCHA
// 403 is a sign-in problem, not a permission one, and keeps forbidden.
func writeScope(err error) error {
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusForbidden && e.Reason == ReasonForbidden && !isCaptcha(e) {
		e.Reason = ReasonNoWriteScope
	}
	return err
}

func isCaptcha(e *Error) bool {
	return e.Err != nil && strings.HasPrefix(e.Err.Error(), captchaPrefix)
}
