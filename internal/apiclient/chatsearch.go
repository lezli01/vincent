package apiclient

import (
	"context"
	"net/url"
	"strconv"
)

// ChatSearchResult is one chat GET /v1/chats/search found (task 158): the
// chat as the list renders it, the places it matched in conversation order
// (title first), and how many there were before the per-chat cap.
type ChatSearchResult struct {
	Chat       Chat              `json:"chat"`
	Matches    []ChatSearchMatch `json:"matches"`
	MatchCount int               `json:"match_count"`
}

// ChatSearchMatch is one place a chat matched: its title (Seq 0, Field
// "title") or a turn's "prompt" or "result". Snippet is the plain text;
// Segments is the same text split into runs, Match marking the ones that
// matched the query.
type ChatSearchMatch struct {
	Seq      int                 `json:"seq"`
	Field    string              `json:"field"`
	Snippet  string              `json:"snippet"`
	Segments []ChatSearchSegment `json:"segments"`
}

// ChatSearchSegment is one run of a snippet.
type ChatSearchSegment struct {
	Text  string `json:"text"`
	Match bool   `json:"match,omitempty"`
}

// The fields a ChatSearchMatch can be in.
const (
	ChatSearchFieldTitle  = "title"
	ChatSearchFieldPrompt = "prompt"
	ChatSearchFieldResult = "result"
)

// CodeQueryTooShort is the error code of a search whose terms are shorter
// than the index can answer (three characters).
const CodeQueryTooShort = "query_too_short"

// SearchChatsOptions scopes a search. Archived is "false", "true" or
// "all"; empty means the server's default, which for search is "all".
// ProjectID 0 searches every project.
type SearchChatsOptions struct {
	ProjectID int64
	Archived  string
	Limit     int
}

// SearchChats is GET /v1/chats/search.
func (c *Client) SearchChats(ctx context.Context, q string, opts SearchChatsOptions) ([]ChatSearchResult, error) {
	v := url.Values{}
	v.Set("q", q)
	if opts.ProjectID != 0 {
		v.Set("project_id", strconv.FormatInt(opts.ProjectID, 10))
	}
	if opts.Archived != "" {
		v.Set("archived", opts.Archived)
	}
	if opts.Limit > 0 {
		v.Set("limit", strconv.Itoa(opts.Limit))
	}
	var out struct {
		Results []ChatSearchResult `json:"results"`
	}
	if err := c.get(ctx, "/v1/chats/search?"+v.Encode(), &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}
