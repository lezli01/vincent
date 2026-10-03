package taskrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// issueContext maps a task's stored issue snapshot onto `.Issue` (§8.4, task
// 035, task 130 decision 8). The order is fixed:
//
//   - the vincent snapshot (Task.Issue): Number is the vincent id and Source
//     the remote reference, zero for a local issue;
//   - else the legacy GitHub snapshot (Task.GitHubIssue, written by the
//     `github_issue` create path until 130.11 removes it): Number is the
//     GitHub number and Source that same reference, so a template written
//     before the reshape renders byte-identically;
//   - else the zero value.
//
// A task with no linked issue renders the zero value, which is the whole
// point: `{{ if .Issue.Number }}` is what a template shared between linked
// and unlinked tasks tests, exactly as `.Loop.Index` is (decision 8).
//
// This is the only path from a stored issue to a template, and it reads the
// task row — never the network. That is what keeps §8.4's promise that a step
// render cannot fail for an external reason, and it is why an issue edited
// after task creation does not change what a later step renders.
func issueContext(task *store.Task) workflow.IssueContext {
	if task == nil {
		return workflow.IssueContext{}
	}
	if snap := task.Issue; snap != nil {
		ctx := workflow.IssueContext{
			Number:   int(snap.ID),
			Title:    snap.Title,
			Body:     snap.Body,
			State:    snap.State,
			Labels:   slices.Clone(snap.Labels),
			Kind:     snap.Kind,
			Priority: snap.Priority,
			Author:   snap.Author,
			Comments: templateComments(snap.Comments),
		}
		if rem := snap.Remote; rem != nil {
			ctx.Source = workflow.IssueSource{
				Provider: rem.Provider,
				Repo:     rem.Repo,
				Number:   rem.Number,
				URL:      rem.URL,
				State:    rem.State,
			}
			ctx.Repo, ctx.URL = rem.Repo, rem.URL
			if len(rem.Assignees) > 0 {
				ctx.Assignee = rem.Assignees[0]
			}
			ctx.Milestone, ctx.MilestoneNumber = rem.Milestone, rem.MilestoneNumber
		}
		return ctx
	}
	if issue := task.GitHubIssue; issue != nil {
		return workflow.IssueContext{
			Number:          issue.Number,
			Title:           issue.Title,
			Body:            issue.Body,
			State:           issue.State,
			Labels:          slices.Clone(issue.Labels),
			Author:          issue.Author,
			Assignee:        issue.Assignee,
			Milestone:       issue.Milestone,
			MilestoneNumber: issue.MilestoneNumber,
			Source: workflow.IssueSource{
				Provider: providerGitHub,
				Repo:     issue.Repo,
				Number:   issue.Number,
				URL:      issue.URL,
				State:    issue.State,
			},
			Repo: issue.Repo,
			URL:  issue.URL,
		}
	}
	return workflow.IssueContext{}
}

// templateComments maps a snapshot's thread onto `.Issue.Comments` (task 130
// decision 24, 130.16). It reads the frozen snapshot, so a comment written
// after task creation never reaches a render; a legacy GitHub snapshot has no
// thread at all and renders none.
func templateComments(in []store.IssueSnapshotComment) []workflow.IssueComment {
	if len(in) == 0 {
		return nil
	}
	out := make([]workflow.IssueComment, 0, len(in))
	for _, c := range in {
		out = append(out, workflow.IssueComment{Author: c.Author, Body: c.Body, CreatedAt: c.CreatedAt})
	}
	return out
}

// providerGitHub is the Source.Provider of a legacy GitHub snapshot — the
// same spelling issue_remotes.provider uses for an imported GitHub issue.
const providerGitHub = "github"

// cloneIssue copies a snapshot for a task that inherits it — a `fan_out` lane
// (§7.6, task 035 decision 9). Lanes already inherit `Fields`, and a lane
// prompt that could read `.Task.Fields` but not `.Issue` would be an
// arbitrary hole. The copy is deep because the two rows are independent
// tasks: nothing today mutates a snapshot, and sharing a slice between rows
// is not a property worth relying on.
func cloneIssue(issue *github.Issue) *github.Issue {
	if issue == nil {
		return nil
	}
	copied := *issue
	copied.Labels = slices.Clone(issue.Labels)
	return &copied
}

// cloneInt64 copies a nullable id, so a lane's IssueID is its own pointer
// rather than an alias of the parent row's field.
func cloneInt64(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// issueFileName is the snapshot file's name inside the worktree's own git
// directory (130.14). One per worktree, so one per task.
const issueFileName = "vincent-issue.json"

// issueFile is $VINCENT_ISSUE_FILE's shape (§8.5, 130.14): `gh issue view
// --json number,title,body,url,createdAt,state,stateReason,labels,author,
// comments` key for key, so a workflow's existing jq keeps working and a
// `fetch` step becomes a copy, plus vincent's own extras.
//
// Number is the **GitHub** number and null for a local issue — the file's
// contract is gh's, which is why it does not follow `.Issue.Number` into
// being the vincent id (task 130 decision 8). Comments is the snapshot's
// thread in gh's element shape, oldest first (130.16, closing decision
// 23.2's gap) — always an array, `[]` when the thread is empty and for a
// legacy GitHub snapshot, which never carried one.
type issueFile struct {
	Number      *int               `json:"number"`
	Title       string             `json:"title"`
	Body        string             `json:"body"`
	URL         string             `json:"url"`
	CreatedAt   *time.Time         `json:"createdAt"`
	State       string             `json:"state"`
	StateReason *string            `json:"stateReason"`
	Labels      []issueFileLabel   `json:"labels"`
	Author      issueFileAuthor    `json:"author"`
	Comments    []issueFileComment `json:"comments"`

	ID       *int64           `json:"id"`
	Kind     string           `json:"kind"`
	Priority int              `json:"priority"`
	Source   *issueFileSource `json:"source"`
}

type issueFileLabel struct {
	Name string `json:"name"`
}

type issueFileAuthor struct {
	Login string `json:"login"`
}

type issueFileComment struct {
	Author    issueFileAuthor `json:"author"`
	Body      string          `json:"body"`
	CreatedAt time.Time       `json:"createdAt"`
}

type issueFileSource struct {
	Provider string `json:"provider"`
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	URL      string `json:"url"`
}

// issueFileOf maps a task's snapshot onto issueFile, in issueContext's order:
// the vincent snapshot, else the legacy GitHub one. ok is false for a task
// with neither, which gets no file and no VINCENT_ISSUE_* variables. The
// IssueEnv it returns carries everything but the path.
//
// Like issueContext it reads the row and never the network (decision 5).
func issueFileOf(task *store.Task) (issueFile, workflow.IssueEnv, bool) {
	if task == nil {
		return issueFile{}, workflow.IssueEnv{}, false
	}
	if snap := task.Issue; snap != nil {
		f := issueFile{
			Title:    snap.Title,
			Body:     snap.Body,
			State:    strings.ToUpper(snap.State),
			Labels:   fileLabels(snap.Labels),
			Author:   issueFileAuthor{Login: snap.Author},
			Comments: fileComments(snap.Comments),
			ID:       &snap.ID,
			Kind:     snap.Kind,
			Priority: snap.Priority,
		}
		if !snap.CreatedAt.IsZero() {
			f.CreatedAt = &snap.CreatedAt
		}
		if snap.State == string(issuestate.Closed) {
			reason := snap.CloseReason
			if reason == "" {
				reason = string(issuestate.Completed)
			}
			reason = strings.ToUpper(reason)
			f.StateReason = &reason
		}
		ie := workflow.IssueEnv{ID: snap.ID}
		if rem := snap.Remote; rem != nil {
			n := rem.Number
			f.Number, f.URL = &n, rem.URL
			f.Source = &issueFileSource{Provider: rem.Provider, Repo: rem.Repo, Number: rem.Number, URL: rem.URL}
			ie.Number, ie.URL = rem.Number, rem.URL
		}
		return f, ie, true
	}
	if issue := task.GitHubIssue; issue != nil {
		n := issue.Number
		f := issueFile{
			Number:   &n,
			Title:    issue.Title,
			Body:     issue.Body,
			URL:      issue.URL,
			State:    strings.ToUpper(issue.State),
			Labels:   fileLabels(issue.Labels),
			Author:   issueFileAuthor{Login: issue.Author},
			Comments: []issueFileComment{},
			Source:   &issueFileSource{Provider: providerGitHub, Repo: issue.Repo, Number: issue.Number, URL: issue.URL},
		}
		if !issue.CreatedAt.IsZero() {
			created := issue.CreatedAt.UTC()
			f.CreatedAt = &created
		}
		return f, workflow.IssueEnv{Number: issue.Number, URL: issue.URL}, true
	}
	return issueFile{}, workflow.IssueEnv{}, false
}

// fileComments maps a snapshot's thread onto gh's `comments` elements. The
// result is never nil, so the file says `[]` rather than null — what
// `gh issue view --json comments` prints for an issue with no thread.
func fileComments(in []store.IssueSnapshotComment) []issueFileComment {
	out := make([]issueFileComment, 0, len(in))
	for _, c := range in {
		out = append(out, issueFileComment{Author: issueFileAuthor{Login: c.Author}, Body: c.Body, CreatedAt: c.CreatedAt.UTC()})
	}
	return out
}

func fileLabels(names []string) []issueFileLabel {
	out := make([]issueFileLabel, 0, len(names))
	for _, n := range names {
		out = append(out, issueFileLabel{Name: n})
	}
	return out
}

// writeIssueFile writes the task's snapshot to its worktree's git directory
// and returns the §8.5 variables' input; the zero IssueEnv for an unlinked
// task (130.14).
//
// The git directory — `{repo}/.git/worktrees/{name}` for a linked worktree —
// is outside the working tree, so `git add -A` can never stage the file; it
// lives under the repository, which a containerized step already mounts at
// its own path (task 061 decision 2); and `git worktree remove` takes it
// away. It is rewritten before every attempt rather than once: it is a pure
// function of the task row, so recovery, a retry and a follow-up all see the
// right file with no state recording that it was written. A file already
// holding the same bytes is left alone, which is what makes the concurrent
// members of a `parallel` group — one worktree, one snapshot — safe.
func writeIssueFile(task *store.Task) (workflow.IssueEnv, error) {
	f, ie, ok := issueFileOf(task)
	if !ok {
		return workflow.IssueEnv{}, nil
	}
	gitDir, err := worktreeGitDir(task.WorktreePath)
	if err != nil {
		return workflow.IssueEnv{}, err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return workflow.IssueEnv{}, fmt.Errorf("marshal issue file: %w", err)
	}
	b = append(b, '\n')
	path := filepath.Join(gitDir, issueFileName)
	//nolint:gosec // G304: issueFileName in the git dir of the task's own worktree
	if cur, rErr := os.ReadFile(path); rErr != nil || !bytes.Equal(cur, b) {
		if err := writeFileAtomic(path, b); err != nil {
			return workflow.IssueEnv{}, err
		}
	}
	ie.File = path
	return ie, nil
}

// worktreeGitDir resolves a worktree's own git directory without running
// git: a linked worktree's `.git` is a file reading `gitdir: <path>`, a
// main checkout's is the directory itself.
func worktreeGitDir(worktreePath string) (string, error) {
	if worktreePath == "" {
		return "", errors.New("resolve git dir: the task has no worktree")
	}
	dotGit := filepath.Join(worktreePath, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", fmt.Errorf("resolve git dir: %w", err)
	}
	if info.IsDir() {
		return dotGit, nil
	}
	//nolint:gosec // G304: the `.git` file of the task's own worktree
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return "", fmt.Errorf("resolve git dir: %w", err)
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return "", fmt.Errorf("resolve git dir: %s is not a gitdir file", dotGit)
	}
	dir = filepath.FromSlash(strings.TrimSpace(dir))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(worktreePath, dir)
	}
	return filepath.Clean(dir), nil
}

// writeFileAtomic replaces path with b through a sibling temporary file, so
// a step reading the file never sees it half-written.
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), issueFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("write issue file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("write issue file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write issue file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write issue file: %w", err)
	}
	return nil
}
