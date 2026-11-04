package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/lezli01/vincent/internal/jira/jiramap"
)

// Visibility restricts a comment to a role or group.
type Visibility struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Comment is one comment on an issue. RenderedBody is Jira's HTML; Raw is the
// comment exactly as answered, for a caller that keeps the remote record.
// Visibility is set on a restricted comment, which Comments returns and its
// caller drops.
type Comment struct {
	ID           string
	Author       jiramap.User
	RenderedBody string
	Created      time.Time
	Updated      time.Time
	Visibility   *Visibility
	Raw          json.RawMessage
}

// commentPage is GET issue/{id}/comment's answer, which — unlike search/jql
// — pages by startAt with a total, on every edition.
type commentPage struct {
	StartAt    int               `json:"startAt"`
	MaxResults int               `json:"maxResults"`
	Total      int               `json:"total"`
	Comments   []json.RawMessage `json:"comments"`
}

type wireComment struct {
	ID           string       `json:"id"`
	Author       jiramap.User `json:"author"`
	RenderedBody string       `json:"renderedBody"`
	Created      string       `json:"created"`
	Updated      string       `json:"updated"`
	Visibility   *Visibility  `json:"visibility"`
}

func parseComment(raw json.RawMessage) (Comment, error) {
	var w wireComment
	if err := json.Unmarshal(raw, &w); err != nil {
		return Comment{}, err
	}
	return Comment{
		ID: w.ID, Author: w.Author, RenderedBody: w.RenderedBody,
		Created: parseTime(w.Created), Updated: parseTime(w.Updated),
		Visibility: w.Visibility, Raw: append(json.RawMessage(nil), raw...),
	}, nil
}

// Comments reads every comment on an issue, oldest first, with
// expand=renderedBody, following every page. Restricted comments are
// returned with Visibility set.
func (c *Client) Comments(ctx context.Context, issueID string) ([]Comment, error) {
	q := url.Values{}
	q.Set("orderBy", commentOrder)
	q.Set("expand", expandRenderedBody)
	q.Set("maxResults", strconv.Itoa(commentPageSize))
	var out []Comment
	startAt := 0
	for {
		q.Set("startAt", strconv.Itoa(startAt))
		var page commentPage
		if err := c.do(ctx, "comments", http.MethodGet, pathComments(issueID), q, nil, &page); err != nil {
			return nil, err
		}
		for _, raw := range page.Comments {
			cm, err := parseComment(raw)
			if err != nil {
				return nil, &Error{Op: "comments", Status: http.StatusOK, Reason: ReasonBadResponse, Err: fmt.Errorf("decode comment: %w", err)}
			}
			out = append(out, cm)
		}
		startAt += len(page.Comments)
		if len(page.Comments) == 0 || startAt >= page.Total {
			return out, nil
		}
	}
}
