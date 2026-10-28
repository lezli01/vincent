package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// HandoffContext is `.Handoff` — the handoff a `role: handoff` task is
// integrating (§8.4, task 137.8, task 137 decision 7). Its zero value — `ID:
// 0` — is what every other task renders with, exactly as `.Issue`'s
// `Number: 0` works, so `{{ if .Handoff.ID }}` tells the two apart.
//
// It is built from the handoff's rows — the items' claim-time issue and
// delivery snapshots — never from the network and never from the live issue,
// so a retry renders what the first attempt did.
//
// ItemLines is the second structured value in §8.4 beside `.Item`: one
// compact JSON object per item, newline-separated, each one element of
// `$VINCENT_HANDOFF_FILE`'s `items`. It exists so a derived `fan_out` can
// write `for_each: '{{ .Handoff.ItemLines }}'` and read `.Item.issue.id` and
// `.Item.branch` in its `lane:` template — task 080's path unchanged and no
// §8.4 FuncMap (task 016 decision 15). Items is the typed list a prompt
// ranges over.
type HandoffContext struct {
	ID        int64
	Grant     []string
	Items     []HandoffItemContext
	ItemLines string
}

// HandoffItemContext is one element of `.Handoff.Items`: the claimed issue
// in `.Issue`'s shape, the delivery it was handed with, and that delivery's
// task, branch and commit.
type HandoffItemContext struct {
	Issue    IssueContext
	Delivery DeliveryContext
	TaskID   int64
	Branch   string
	HeadSHA  string
}

// DeliveryContext is `.Handoff.Items[].Delivery`, the snapshotted delivery
// (§5.3, task 137 decision 3).
type DeliveryContext struct {
	Kind        string
	Title       string
	Body        string
	Summary     string
	HeadSHA     string
	Branch      string
	Base        string
	CloseReason string
	Proof       string
	Ready       bool
}

// HandoffEnv is what Env needs to export `$VINCENT_HANDOFF_FILE` (§8.5).
// Its zero value — no File — is every task that is not a handoff task.
type HandoffEnv struct {
	// File is the absolute path of the already-written handoff file.
	File string
}

// HandoffItemLines joins a handoff file's `items` elements into ItemLines:
// each compacted onto one line, in file order. Both the engine, from the
// file it writes, and `vincent workflow render --handoff-file`, from the
// file it reads, go through it, so the two cannot drift.
func HandoffItemLines(items []json.RawMessage) (string, error) {
	lines := make([]string, 0, len(items))
	for i, raw := range items {
		var b bytes.Buffer
		if err := json.Compact(&b, raw); err != nil {
			return "", fmt.Errorf("handoff item %d: %w", i, err)
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n"), nil
}

// handoffFileShape is the part of `$VINCENT_HANDOFF_FILE` a preview reads.
// The writer is internal/taskrun's; this restates its keys so a file — one a
// run wrote, or one an author wrote by hand — binds as `.Handoff` offline.
type handoffFileShape struct {
	ID    int64             `json:"id"`
	Grant []string          `json:"grant"`
	Items []json.RawMessage `json:"items"`
}

type handoffFileItemShape struct {
	Issue struct {
		Number *int   `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Comments []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			Body      string    `json:"body"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"comments"`
		ID       *int64 `json:"id"`
		Kind     string `json:"kind"`
		Priority int    `json:"priority"`
		Source   *struct {
			Provider string `json:"provider"`
			Repo     string `json:"repo"`
			Number   int    `json:"number"`
			URL      string `json:"url"`
		} `json:"source"`
	} `json:"issue"`
	Delivery struct {
		Kind        string `json:"kind"`
		Title       string `json:"title"`
		Body        string `json:"body"`
		Summary     string `json:"summary"`
		HeadSHA     string `json:"head_sha"`
		Branch      string `json:"branch"`
		Base        string `json:"base"`
		CloseReason string `json:"close_reason"`
		Proof       string `json:"proof"`
		Ready       bool   `json:"ready"`
	} `json:"delivery"`
	TaskID  *int64 `json:"task_id"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"head_sha"`
}

// ParseHandoffFile reads a file in `$VINCENT_HANDOFF_FILE`'s shape and
// returns the `.Handoff` it stands for — what `vincent workflow render
// --handoff-file` binds (task 044, task 137.8). The issue is read back from
// its gh-shaped element, so the fields that element does not carry (an
// assignee, a milestone, the provider's own state) preview empty.
func ParseHandoffFile(b []byte) (HandoffContext, error) {
	var f handoffFileShape
	if err := json.Unmarshal(b, &f); err != nil {
		return HandoffContext{}, fmt.Errorf("parse handoff file: %w", err)
	}
	lines, err := HandoffItemLines(f.Items)
	if err != nil {
		return HandoffContext{}, fmt.Errorf("parse handoff file: %w", err)
	}
	hc := HandoffContext{ID: f.ID, Grant: f.Grant, ItemLines: lines}
	for i, raw := range f.Items {
		var it handoffFileItemShape
		if err := json.Unmarshal(raw, &it); err != nil {
			return HandoffContext{}, fmt.Errorf("parse handoff file: item %d: %w", i, err)
		}
		iss := IssueContext{
			Title: it.Issue.Title, Body: it.Issue.Body, State: strings.ToLower(it.Issue.State),
			Kind: it.Issue.Kind, Priority: it.Issue.Priority, Author: it.Issue.Author.Login,
		}
		if it.Issue.ID != nil {
			iss.Number = int(*it.Issue.ID)
		}
		for _, l := range it.Issue.Labels {
			iss.Labels = append(iss.Labels, l.Name)
		}
		for _, c := range it.Issue.Comments {
			iss.Comments = append(iss.Comments, IssueComment{Author: c.Author.Login, Body: c.Body, CreatedAt: c.CreatedAt})
		}
		if src := it.Issue.Source; src != nil {
			iss.Source = IssueSource{Provider: src.Provider, Repo: src.Repo, Number: src.Number, URL: src.URL}
			iss.Repo, iss.URL = src.Repo, src.URL
		}
		d := it.Delivery
		item := HandoffItemContext{
			Issue: iss,
			Delivery: DeliveryContext{
				Kind: d.Kind, Title: d.Title, Body: d.Body, Summary: d.Summary, HeadSHA: d.HeadSHA,
				Branch: d.Branch, Base: d.Base, CloseReason: d.CloseReason, Proof: d.Proof, Ready: d.Ready,
			},
			Branch: it.Branch, HeadSHA: it.HeadSHA,
		}
		if it.TaskID != nil {
			item.TaskID = *it.TaskID
		}
		hc.Items = append(hc.Items, item)
	}
	return hc, nil
}
