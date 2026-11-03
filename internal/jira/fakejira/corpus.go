package fakejira

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// TimeLayout is Jira's timestamp shape: millisecond precision and a numeric
// zone without a colon, `2026-08-26T19:30:00.000+0000` (Jira Cloud REST v3,
// every date-time field).
const TimeLayout = "2006-01-02T15:04:05.000-0700"

// The status categories by their real keys (GET /rest/api/3/statuscategory).
// The ids and colour names are Jira Cloud's: they are global, not per site.
const (
	CategoryNew           = "new"
	CategoryIndeterminate = "indeterminate"
	CategoryDone          = "done"
)

// DefaultUser and DefaultToken are the credentials a Store accepts when it is
// configured with none.
const (
	DefaultUser  = "vincent@example.com"
	DefaultToken = "fake-jira-token"
)

// ErrNoCorpusFile is a mutation helper on a Store with no Path: there is
// nowhere to keep the change.
var ErrNoCorpusFile = errors.New("fakejira: the store has no corpus file")

// ErrNotFound is a mutation helper naming an issue or comment the corpus does
// not hold.
var ErrNotFound = errors.New("fakejira: not found")

// Corpus is the whole fake Jira site.
type Corpus struct {
	Site     Site      `json:"site"`
	Projects []Project `json:"projects"`
	Issues   []Issue   `json:"issues"`
	Comments []Comment `json:"comments"`
	Fake     State     `json:"_fake,omitzero"`
}

// State is the corpus's fake-only state, never part of an answer.
type State struct {
	// SearchLag is how many more search calls answer the stale view.
	SearchLag int `json:"searchLag,omitempty"`
}

// Site is the Jira Cloud site the corpus is.
type Site struct {
	// BaseURL is serverInfo's baseUrl; empty, the request's own origin.
	BaseURL     string `json:"baseUrl,omitempty"`
	ServerTitle string `json:"serverTitle,omitempty"`
	// Myself is the account the credentials authenticate as, and the author
	// of every comment POSTed.
	Myself User `json:"myself"`
}

// User is an Atlassian account.
type User struct {
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress,omitempty"`
}

// Project is one Jira project and its workflow graph.
type Project struct {
	ID          string       `json:"id"`
	Key         string       `json:"key"`
	Name        string       `json:"name"`
	Statuses    []Status     `json:"statuses"`
	Transitions []Transition `json:"transitions"`
}

// Status is a workflow status and its category key.
type Status struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// Transition is one workflow edge. From lists the status ids it leaves;
// empty, it is global and available from every status but its own target.
type Transition struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	From   []string      `json:"from,omitempty"`
	To     string        `json:"to"`
	Fields []ScreenField `json:"fields,omitempty"`
}

// ScreenField is a field on a transition's screen.
type ScreenField struct {
	Key           string    `json:"key"`
	Name          string    `json:"name"`
	Required      bool      `json:"required"`
	AllowedValues []Allowed `json:"allowedValues,omitempty"`
}

// Allowed is one allowed value of a screen field (a resolution, an option).
type Allowed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Issue is one Jira issue.
type Issue struct {
	ID           string   `json:"id"`
	Key          string   `json:"key"`
	PreviousKeys []string `json:"previousKeys,omitempty"`
	// Project is the key of the project the issue is in now.
	Project   string   `json:"project"`
	Summary   string   `json:"summary"`
	IssueType string   `json:"issueType,omitempty"`
	Priority  string   `json:"priority,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Assignee  *User    `json:"assignee,omitempty"`
	Reporter  *User    `json:"reporter,omitempty"`
	// Description is the ADF document, DescriptionHTML its rendering.
	Description     json.RawMessage `json:"description,omitempty"`
	DescriptionHTML string          `json:"descriptionHtml,omitempty"`
	// Status is a status id in the project's graph.
	Status     string     `json:"status"`
	Resolution *Allowed   `json:"resolution,omitempty"`
	Created    string     `json:"created"`
	Updated    string     `json:"updated"`
	Fake       *IssueFake `json:"_fake,omitempty"`
}

// IssueFake is an issue's fake-only markers.
type IssueFake struct {
	Deleted   bool `json:"deleted,omitempty"`
	Forbidden bool `json:"forbidden,omitempty"`
	// Stale is the pre-write view search-lag answers.
	Stale *Stale `json:"stale,omitempty"`
}

// Stale is the part of an issue the lagging search index still holds.
type Stale struct {
	Status     string   `json:"status"`
	Updated    string   `json:"updated"`
	Resolution *Allowed `json:"resolution,omitempty"`
}

// Comment is one comment on an issue.
type Comment struct {
	ID string `json:"id"`
	// Issue is the issue's id, which survives a move.
	Issue      string          `json:"issue"`
	Author     User            `json:"author"`
	Body       json.RawMessage `json:"body"`
	BodyHTML   string          `json:"bodyHtml"`
	Visibility *Visibility     `json:"visibility,omitempty"`
	Created    string          `json:"created"`
	Updated    string          `json:"updated"`
}

// Visibility restricts a comment to a role or group.
type Visibility struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// builtin is the corpus a Store with no Path answers and a missing file is
// seeded from: one project with a four-status graph, a second to move into,
// and three issues across every category.
const builtin = `{
  "site": {
    "serverTitle": "Jira",
    "myself": {"accountId": "557058:fake-vincent", "displayName": "Vincent Bot", "emailAddress": "vincent@example.com"}
  },
  "projects": [
    {
      "id": "10000", "key": "VIN", "name": "Vincent",
      "statuses": [
        {"id": "10000", "name": "To Do", "category": "new"},
        {"id": "10001", "name": "In Progress", "category": "indeterminate"},
        {"id": "10002", "name": "Done", "category": "done"}
      ],
      "transitions": [
        {"id": "11", "name": "Start progress", "from": ["10000"], "to": "10001"},
        {"id": "21", "name": "Stop progress", "from": ["10001"], "to": "10000"},
        {"id": "31", "name": "Done", "from": ["10000", "10001"], "to": "10002",
         "fields": [{"key": "resolution", "name": "Resolution", "required": true,
                     "allowedValues": [{"id": "10000", "name": "Done"}, {"id": "10001", "name": "Won't Do"}]}]},
        {"id": "41", "name": "Reopen", "from": ["10002"], "to": "10000"}
      ]
    },
    {
      "id": "10001", "key": "OPS", "name": "Operations",
      "statuses": [
        {"id": "10000", "name": "To Do", "category": "new"},
        {"id": "10002", "name": "Done", "category": "done"}
      ],
      "transitions": [
        {"id": "31", "name": "Done", "from": ["10000"], "to": "10002"},
        {"id": "41", "name": "Reopen", "from": ["10002"], "to": "10000"}
      ]
    }
  ],
  "issues": [
    {
      "id": "10001", "key": "VIN-1", "project": "VIN",
      "summary": "Sync Jira issues into vincent", "issueType": "Story", "priority": "Medium",
      "labels": ["sync"],
      "reporter": {"accountId": "557058:fake-reporter", "displayName": "Ada Reporter"},
      "description": {"type": "doc", "version": 1, "content": [{"type": "paragraph", "content": [{"type": "text", "text": "Mirror the board."}]}]},
      "descriptionHtml": "<p>Mirror the board.</p>",
      "status": "10000",
      "created": "2026-08-01T09:00:00.000+0000", "updated": "2026-08-01T09:00:00.000+0000"
    },
    {
      "id": "10002", "key": "VIN-2", "project": "VIN",
      "summary": "Map status categories", "issueType": "Task", "priority": "High",
      "assignee": {"accountId": "557058:fake-vincent", "displayName": "Vincent Bot"},
      "status": "10001",
      "created": "2026-08-02T09:00:00.000+0000", "updated": "2026-08-02T10:00:00.123+0000"
    },
    {
      "id": "10003", "key": "VIN-3", "project": "VIN",
      "summary": "Ship the fake", "issueType": "Task",
      "status": "10002", "resolution": {"id": "10000", "name": "Done"},
      "created": "2026-08-03T09:00:00.000+0000", "updated": "2026-08-03T11:00:00.000+0000"
    }
  ],
  "comments": [
    {
      "id": "20001", "issue": "10001",
      "author": {"accountId": "557058:fake-reporter", "displayName": "Ada Reporter"},
      "body": {"type": "doc", "version": 1, "content": [{"type": "paragraph", "content": [{"type": "text", "text": "First."}]}]},
      "bodyHtml": "<p>First.</p>",
      "created": "2026-08-01T09:30:00.000+0000", "updated": "2026-08-01T09:30:00.000+0000"
    },
    {
      "id": "20002", "issue": "10001",
      "author": {"accountId": "557058:fake-reporter", "displayName": "Ada Reporter"},
      "body": {"type": "doc", "version": 1, "content": [{"type": "paragraph", "content": [{"type": "text", "text": "Admins only."}]}]},
      "bodyHtml": "<p>Admins only.</p>",
      "visibility": {"type": "role", "value": "Administrators"},
      "created": "2026-08-01T09:40:00.000+0000", "updated": "2026-08-01T09:40:00.000+0000"
    }
  ]
}`

// Builtin returns a fresh copy of the built-in corpus, for a test that wants
// to start from it and write its own file.
func Builtin() Corpus {
	var c Corpus
	if err := json.Unmarshal([]byte(builtin), &c); err != nil {
		panic("fakejira: built-in corpus: " + err.Error())
	}
	return c
}

// Store is one fake Jira site. A *Store is the http.Handler both legs mount;
// the zero value serves the built-in corpus with the default credentials and
// keeps no write.
type Store struct {
	// Path is the corpus file. Empty, the built-in corpus answers and a write
	// is answered but not kept; set but missing, the built-in corpus seeds it.
	Path string
	// ScenarioFile names the file the scenario is read from on every request;
	// empty or missing, no scenario.
	ScenarioFile string
	// RequestLog, when set, gets one `METHOD path status` line per request.
	RequestLog string
	// User and Token are the Basic credentials accepted; DefaultUser and
	// DefaultToken when empty.
	User, Token string
	// Now stamps a write's updated and created; time.Now when nil.
	Now func() time.Time
	// OnShutdown is called, once the answer is written, by POST
	// /_fake/shutdown. Nil, the route is a 404.
	OnShutdown func()

	// mu serialises a request's load-modify-save: httptest serves requests
	// concurrently, and two writes must not each save a corpus missing the
	// other's change.
	mu sync.Mutex
}

var _ http.Handler = (*Store)(nil)

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) stamp() string { return s.now().Format(TimeLayout) }

// Load reads the corpus, seeding a missing file from the built-in one.
func (s *Store) Load() (Corpus, error) {
	if s.Path == "" {
		return Builtin(), nil
	}
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		c := Builtin()
		return c, s.Save(c)
	}
	if err != nil {
		return Corpus{}, err
	}
	var c Corpus
	if err := json.Unmarshal(raw, &c); err != nil {
		return Corpus{}, fmt.Errorf("fakejira: %s: %w", s.Path, err)
	}
	return c, nil
}

// Save writes the corpus back through a temp file beside it and a rename, so
// a reader sees the old corpus or the new one and never half of either.
// Windows refuses a rename over a file another process has open (a test
// reading the corpus), so the rename is retried briefly rather than failed.
func (s *Store) Save(c Corpus) error {
	if s.Path == "" {
		return nil
	}
	encoded, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.Path, encoded)
}

// WriteFileAtomic writes data to path through a temp file and a rename, the
// way the corpus is saved; cmd/fakejira writes its port file with it.
func WriteFileAtomic(path string, data []byte) error { return writeAtomic(path, data) }

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fakejira-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	for attempt := 0; ; attempt++ {
		err = os.Rename(tmp.Name(), path)
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

// Update loads the corpus, applies fn and saves the result, under the same
// lock the handler holds. It needs a Path.
func (s *Store) Update(fn func(*Corpus) error) error {
	if s.Path == "" {
		return ErrNoCorpusFile
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(&c); err != nil {
		return err
	}
	return s.Save(c)
}

// Move moves an issue to project with newKey: the id is unchanged, the old key
// joins previousKeys and still resolves. The status is kept when the new
// project's graph has it, and becomes the graph's first status otherwise.
func (s *Store) Move(idOrKey, project, newKey string) error {
	return s.Update(func(c *Corpus) error {
		is := c.issue(idOrKey)
		p := c.project(project)
		if is == nil || p == nil {
			return ErrNotFound
		}
		is.PreviousKeys = append(is.PreviousKeys, is.Key)
		is.Key = newKey
		is.Project = p.Key
		if p.status(is.Status) == nil && len(p.Statuses) > 0 {
			is.Status = p.Statuses[0].ID
		}
		is.Updated = s.stamp()
		return nil
	})
}

// Delete marks an issue deleted: 404 on every route, absent from every list.
func (s *Store) Delete(idOrKey string) error {
	return s.mark(idOrKey, func(f *IssueFake) { f.Deleted = true })
}

// Forbid marks an issue forbidden to the credentials: from outside,
// indistinguishable from deleted.
func (s *Store) Forbid(idOrKey string) error {
	return s.mark(idOrKey, func(f *IssueFake) { f.Forbidden = true })
}

func (s *Store) mark(idOrKey string, fn func(*IssueFake)) error {
	return s.Update(func(c *Corpus) error {
		is := c.issue(idOrKey)
		if is == nil {
			return ErrNotFound
		}
		if is.Fake == nil {
			is.Fake = &IssueFake{}
		}
		fn(is.Fake)
		return nil
	})
}

// AddComment files a comment on an issue — the ADF body and its HTML both,
// as the corpus stores them — by the site's own account, and returns its id.
func (s *Store) AddComment(idOrKey string, body json.RawMessage, html string, vis *Visibility) (string, error) {
	var id string
	err := s.Update(func(c *Corpus) error {
		is := c.issue(idOrKey)
		if is == nil {
			return ErrNotFound
		}
		id = c.nextCommentID()
		now := s.stamp()
		c.Comments = append(c.Comments, Comment{
			ID: id, Issue: is.ID, Author: c.Site.Myself, Body: body, BodyHTML: html,
			Visibility: vis, Created: now, Updated: now,
		})
		is.Updated = now
		return nil
	})
	return id, err
}

// EditComment rewrites a comment's body and moves its updated forward.
func (s *Store) EditComment(id string, body json.RawMessage, html string) error {
	return s.Update(func(c *Corpus) error {
		for i := range c.Comments {
			if c.Comments[i].ID == id {
				c.Comments[i].Body, c.Comments[i].BodyHTML = body, html
				c.Comments[i].Updated = s.stamp()
				return nil
			}
		}
		return ErrNotFound
	})
}

// RemoveComment deletes a comment.
func (s *Store) RemoveComment(id string) error {
	return s.Update(func(c *Corpus) error {
		for i := range c.Comments {
			if c.Comments[i].ID == id {
				c.Comments = append(c.Comments[:i], c.Comments[i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}

// Transition applies a transition the way the REST POST does, screen-field
// validation included; a rejection is a *FieldErrors.
func (s *Store) Transition(idOrKey, transitionID string, fields map[string]any) error {
	return s.Update(func(c *Corpus) error {
		is := c.issue(idOrKey)
		if is == nil {
			return ErrNotFound
		}
		return c.applyTransition(is, transitionID, fields, s.stamp())
	})
}

// SetUpdated sets an issue's updated.
func (s *Store) SetUpdated(idOrKey string, t time.Time) error {
	return s.Update(func(c *Corpus) error {
		is := c.issue(idOrKey)
		if is == nil {
			return ErrNotFound
		}
		is.Updated = t.UTC().Format(TimeLayout)
		return nil
	})
}

// issue finds an issue by id, current key or previous key, hidden or not.
func (c *Corpus) issue(idOrKey string) *Issue {
	for i := range c.Issues {
		if c.Issues[i].ID == idOrKey || c.Issues[i].Key == idOrKey {
			return &c.Issues[i]
		}
	}
	for i := range c.Issues {
		for _, k := range c.Issues[i].PreviousKeys {
			if k == idOrKey {
				return &c.Issues[i]
			}
		}
	}
	return nil
}

func (c *Corpus) project(key string) *Project {
	for i := range c.Projects {
		if c.Projects[i].Key == key {
			return &c.Projects[i]
		}
	}
	return nil
}

func (p *Project) status(id string) *Status {
	for i := range p.Statuses {
		if p.Statuses[i].ID == id {
			return &p.Statuses[i]
		}
	}
	return nil
}

// available is the transitions the graph allows from status.
func (p *Project) available(status string) []Transition {
	var out []Transition
	for _, t := range p.Transitions {
		if len(t.From) == 0 {
			if t.To != status {
				out = append(out, t)
			}
			continue
		}
		for _, f := range t.From {
			if f == status {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

func (c *Corpus) nextCommentID() string {
	highest := 0
	for _, cm := range c.Comments {
		if n, err := strconv.Atoi(cm.ID); err == nil && n > highest {
			highest = n
		}
	}
	return strconv.Itoa(highest + 1)
}

// hidden is deleted or forbidden: the two look the same from outside.
func (is *Issue) hidden() bool {
	return is.Fake != nil && (is.Fake.Deleted || is.Fake.Forbidden)
}

// FieldErrors is a transition's screen rejection: Jira's `errors` map.
type FieldErrors struct {
	Messages []string
	Fields   map[string]string
}

func (e *FieldErrors) Error() string {
	return fmt.Sprintf("fakejira: transition rejected: %v %v", e.Messages, e.Fields)
}

// applyTransition moves is along transitionID, validating the screen. A
// resolution the screen supplies is set; a move to a status outside the done
// category clears the resolution, as Jira's default workflow does on reopen.
func (c *Corpus) applyTransition(is *Issue, transitionID string, fields map[string]any, now string) error {
	p := c.project(is.Project)
	if p == nil {
		return &FieldErrors{Messages: []string{"The issue's project has no workflow."}}
	}
	var tr *Transition
	for _, t := range p.available(is.Status) {
		if t.ID == transitionID {
			tr = &t
			break
		}
	}
	if tr == nil {
		return &FieldErrors{Messages: []string{fmt.Sprintf("Transition id '%s' is not valid for this issue.", transitionID)}}
	}
	errs := map[string]string{}
	var resolution *Allowed
	for _, f := range tr.Fields {
		v, ok := fields[f.Key]
		if !ok || v == nil {
			if f.Required {
				errs[f.Key] = f.Name + " is required."
			}
			continue
		}
		if len(f.AllowedValues) > 0 {
			a := matchAllowed(f.AllowedValues, v)
			if a == nil {
				errs[f.Key] = fmt.Sprintf("Specify a valid value for %s", f.Name)
				continue
			}
			if f.Key == "resolution" {
				resolution = a
			}
		}
	}
	for k := range fields {
		if !tr.hasField(k) {
			errs[k] = fmt.Sprintf("Field '%s' cannot be set. It is not on the appropriate screen, or unknown.", k)
		}
	}
	if len(errs) > 0 {
		return &FieldErrors{Fields: errs}
	}
	is.Status = tr.To
	if resolution != nil {
		is.Resolution = resolution
	} else if st := p.status(tr.To); st == nil || st.Category != CategoryDone {
		is.Resolution = nil
	}
	is.Updated = now
	return nil
}

func (t *Transition) hasField(key string) bool {
	for _, f := range t.Fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

// matchAllowed matches a field value — `{"id": …}`, `{"name": …}` or a bare
// string — against allowed values.
func matchAllowed(allowed []Allowed, v any) *Allowed {
	var id, name string
	switch x := v.(type) {
	case string:
		id, name = x, x
	case map[string]any:
		id, _ = x["id"].(string)
		name, _ = x["name"].(string)
	}
	for i := range allowed {
		if (id != "" && allowed[i].ID == id) || (name != "" && allowed[i].Name == name) {
			return &allowed[i]
		}
	}
	return nil
}
