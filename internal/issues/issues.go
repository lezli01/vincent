package issues

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// Input bounds (task 130 decision 4).
const (
	// MaxTitleBytes is the same bound internal/api/limits.go puts on a task
	// title (maxTitleBytes). This package cannot import internal/api, so the
	// value is repeated here; keep the two equal.
	MaxTitleBytes = 1 << 10
	MaxLabelBytes = 64
	MaxKindBytes  = 32
	// MinPriority..MaxPriority is Linear's scale: 0 none, 1 urgent … 4 low.
	// It is inverted relative to a task's priority (decision 4).
	MinPriority = 0
	MaxPriority = 4
)

// kindPattern is the shape of a non-empty kind: a lowercase token a
// workflow can branch on (decision 4).
var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ValidationError is a refused input. Field names the offending field
// ("title", "labels", "kind", "priority", "by", "reason", "action", "body");
// the API maps it to a 400.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Message)
}

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Service is the single write path for issues. It is safe for concurrent
// use: it holds nothing but the store.
type Service struct {
	st *store.Store
}

// New returns a Service writing through s.
func New(s *store.Store) *Service {
	return &Service{st: s}
}

// CreateInput is a new issue as a caller asks for it, before validation.
type CreateInput struct {
	ProjectID int64
	Title     string
	Body      string
	Kind      string
	Author    string
	Priority  int
	Labels    []string
}

// Create validates in and creates the issue, attributed to by.
func (s *Service) Create(ctx context.Context, by issuestate.Actor, in CreateInput) (*store.Issue, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	title, err := cleanTitle(in.Title)
	if err != nil {
		return nil, err
	}
	if err := checkKind(in.Kind); err != nil {
		return nil, err
	}
	if err := checkPriority(in.Priority); err != nil {
		return nil, err
	}
	labels, err := cleanLabels(in.Labels)
	if err != nil {
		return nil, err
	}
	return s.st.CreateIssue(ctx, store.NewIssue{
		ProjectID: in.ProjectID,
		Title:     title,
		Body:      in.Body,
		Kind:      in.Kind,
		Author:    in.Author,
		Priority:  in.Priority,
		Labels:    labels,
	}, by)
}

// Get reads one issue; store.ErrNotFound when it does not exist.
func (s *Service) Get(ctx context.Context, id int64) (*store.Issue, error) {
	return s.st.GetIssue(ctx, id)
}

// List reads the issues f selects, newest updated first.
func (s *Service) List(ctx context.Context, f store.IssueFilter) ([]*store.Issue, error) {
	return s.st.ListIssues(ctx, f)
}

// Update validates the fields p sets and applies them if the issue is still
// at version; store.ErrIssueChanged when it is not.
func (s *Service) Update(ctx context.Context, by issuestate.Actor, id, version int64, p store.IssuePatch) (*store.Issue, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	if p.Title != nil {
		title, err := cleanTitle(*p.Title)
		if err != nil {
			return nil, err
		}
		p.Title = &title
	}
	if p.Kind != nil {
		if err := checkKind(*p.Kind); err != nil {
			return nil, err
		}
	}
	if p.Priority != nil {
		if err := checkPriority(*p.Priority); err != nil {
			return nil, err
		}
	}
	return s.st.UpdateIssue(ctx, id, version, p, by)
}

// Close closes the issue with reason; "" means completed.
func (s *Service) Close(ctx context.Context, by issuestate.Actor, id int64, reason issuestate.Reason) (*store.Issue, error) {
	return s.Transition(ctx, by, id, issuestate.Close, reason)
}

// Reopen reopens a closed issue.
func (s *Service) Reopen(ctx context.Context, by issuestate.Actor, id int64) (*store.Issue, error) {
	return s.Transition(ctx, by, id, issuestate.Reopen, "")
}

// Transition is the general entry Close and Reopen are spelled with, and the
// one sync uses for RemoteClosed and RemoteReopened. Whether the action is
// legal from the issue's state for by is the store's to decide, inside its
// transaction: store.ErrInvalidIssueAction when it is not.
func (s *Service) Transition(ctx context.Context, by issuestate.Actor, id int64, action issuestate.Action, reason issuestate.Reason) (*store.Issue, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	if !slices.Contains(issuestate.Actions, action) {
		return nil, invalid("action", "unknown action %q", action)
	}
	resolved, err := issuestate.ResolveReason(action, reason)
	if err != nil {
		return nil, invalid("reason", "%v", err)
	}
	return s.st.TransitionIssue(ctx, id, action, resolved, by)
}

// SetLabels replaces the issue's labels with names.
func (s *Service) SetLabels(ctx context.Context, by issuestate.Actor, id int64, names []string) (*store.Issue, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	labels, err := cleanLabels(names)
	if err != nil {
		return nil, err
	}
	return s.st.SetIssueLabels(ctx, id, labels, by)
}

// Comment adds a local comment to the issue.
func (s *Service) Comment(ctx context.Context, by issuestate.Actor, issueID int64, author, body string) (*store.IssueComment, error) {
	if err := checkActor(by); err != nil {
		return nil, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, invalid("body", "must not be empty")
	}
	return s.st.AddIssueComment(ctx, issueID, author, body, "", by)
}

// Comments reads the issue's comments, oldest first.
func (s *Service) Comments(ctx context.Context, issueID int64) ([]*store.IssueComment, error) {
	return s.st.ListIssueComments(ctx, issueID)
}

// Delete removes the issue.
func (s *Service) Delete(ctx context.Context, by issuestate.Actor, id int64) error {
	if err := checkActor(by); err != nil {
		return err
	}
	return s.st.DeleteIssue(ctx, id, by)
}

func checkActor(by issuestate.Actor) error {
	if !issuestate.ValidActor(by) {
		return invalid("by", "unknown actor %q", by)
	}
	return nil
}

func cleanTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", invalid("title", "must not be empty")
	}
	if len(title) > MaxTitleBytes {
		return "", invalid("title", "is %d bytes, at most %d allowed", len(title), MaxTitleBytes)
	}
	return title, nil
}

func checkKind(kind string) error {
	if kind == "" {
		return nil
	}
	if len(kind) > MaxKindBytes {
		return invalid("kind", "is %d bytes, at most %d allowed", len(kind), MaxKindBytes)
	}
	if !kindPattern.MatchString(kind) {
		return invalid("kind", "%q is not a lowercase token (%s)", kind, kindPattern)
	}
	return nil
}

func checkPriority(p int) error {
	if p < MinPriority || p > MaxPriority {
		return invalid("priority", "%d is outside %d..%d", p, MinPriority, MaxPriority)
	}
	return nil
}

// cleanLabels trims each name and collapses names differing only by case to
// their first spelling, so the store's NOCASE catalogue sees each label once.
func cleanLabels(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			return nil, invalid("labels", "a label name must not be empty")
		}
		if len(n) > MaxLabelBytes {
			return nil, invalid("labels", "%q is %d bytes, at most %d allowed", n, len(n), MaxLabelBytes)
		}
		key := strings.ToLower(n)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
	}
	return out, nil
}
