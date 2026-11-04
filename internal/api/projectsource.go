package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/jira/jiramap"
	"github.com/lezli01/vincent/internal/store"
)

// A project's issue-source binding (task 142.6, issue #902, spec §5.1, §5.6).
// It is daemon state only: stored on the projects row, edited through the
// project routes, and never read from the repository — a checked-in file
// naming a Jira site would let anyone who can merge point the daemon, and
// the user's token, at a host they control (task 142 decision 5.2).

// issueSourceDerive is the wire spelling of the stored "": the source follows
// origin. It is spelled out on the wire so a reader never has to know that an
// empty string means something.
const issueSourceDerive = "derive"

// Bounds on the free-text Jira fields. Nothing finer can be checked without
// asking the site, which #908's probe does.
const (
	maxJiraProjectKey = 255
	maxJiraJQL        = 2000
	maxJiraSite       = 2048
)

var (
	jiraKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	orderByPattern = regexp.MustCompile(`(?i)\border\s+by\b`)
)

// projectSourceResponse is the binding as the project DTO carries it.
// Effective is the daemon's resolution of "derive" against origin, so no
// client derives it.
type projectSourceResponse struct {
	Provider  string             `json:"provider"`
	Effective string             `json:"effective"`
	Jira      *projectSourceJira `json:"jira"`
}

// projectSourceJira is the Jira half of the binding, on the wire in and out.
// Mapping is a jiramap override object, or null for the defaults.
type projectSourceJira struct {
	Site       string          `json:"site"`
	ProjectKey string          `json:"project_key"`
	JQL        string          `json:"jql"`
	Mapping    json.RawMessage `json:"mapping"`
}

// projectSourceRequest is the binding as create and PATCH accept it. It
// replaces the whole binding: a PATCH that changes only the JQL sends the
// site and key again, which is what makes "what is bound" one decision.
type projectSourceRequest struct {
	Provider string             `json:"provider"`
	Jira     *projectSourceJira `json:"jira"`
}

func toProjectSourceResponse(b store.IssueSource, effective string) projectSourceResponse {
	out := projectSourceResponse{Provider: b.Provider, Effective: effective}
	if out.Provider == store.IssueProviderDerive {
		out.Provider = issueSourceDerive
	}
	if b.Provider == store.IssueProviderJira {
		j := &projectSourceJira{Site: b.JiraSite, ProjectKey: b.JiraProjectKey, JQL: b.JiraJQL}
		if b.JiraMappingJSON != "" {
			j.Mapping = json.RawMessage(b.JiraMappingJSON)
		}
		out.Jira = j
	}
	return out
}

// decodeIssueSource strictly decodes the issue_source member of a create or
// PATCH body — the outer decode cannot reach inside it — and validates it.
// null is "derive". The message is a 400 validation_failed's, "" on success.
func decodeIssueSource(raw json.RawMessage) (store.IssueSource, string) {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return store.IssueSource{}, ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req projectSourceRequest
	if err := dec.Decode(&req); err != nil {
		return store.IssueSource{}, fmt.Sprintf("issue_source: %v", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return store.IssueSource{}, "issue_source: trailing data after the object"
	}
	return validateIssueSource(req)
}

// validateIssueSource is the one validator of a binding (task 142.6 decision
// 2). The API calls it and every other surface reaches it through the API.
// It is pure: it reads no repository, no network and no store.
func validateIssueSource(req projectSourceRequest) (store.IssueSource, string) {
	provider := strings.TrimSpace(req.Provider)
	switch provider {
	case issueSourceDerive, "":
		provider = store.IssueProviderDerive
	case store.IssueProviderNone, store.IssueProviderGitHub, store.IssueProviderJira:
	default:
		return store.IssueSource{}, fmt.Sprintf(
			"issue_source.provider %q is not one of derive, none, github, jira", req.Provider)
	}
	if provider != store.IssueProviderJira {
		// Any other provider clears the Jira fields, so a stale site never
		// lingers beside a GitHub binding. Sending them is refused rather
		// than silently dropped: it is a binding the caller did not get.
		if req.Jira != nil {
			return store.IssueSource{}, "issue_source.jira is only allowed with provider jira"
		}
		return store.IssueSource{Provider: provider}, ""
	}
	if req.Jira == nil {
		return store.IssueSource{}, "issue_source.jira is required with provider jira: it needs a site and a project_key"
	}
	site, msg := normalizeJiraSite(req.Jira.Site)
	if msg != "" {
		return store.IssueSource{}, msg
	}
	key := strings.TrimSpace(req.Jira.ProjectKey)
	switch {
	case key == "":
		return store.IssueSource{}, "issue_source.jira.project_key is required with provider jira"
	case len(key) > maxJiraProjectKey:
		return store.IssueSource{}, fmt.Sprintf("issue_source.jira.project_key is longer than %d characters", maxJiraProjectKey)
	case !jiraKeyPattern.MatchString(key):
		return store.IssueSource{}, fmt.Sprintf(
			"issue_source.jira.project_key %q is not a Jira project key: an uppercase letter, then uppercase letters, digits or underscores", key)
	}
	jql, msg := validateJiraJQL(req.Jira.JQL)
	if msg != "" {
		return store.IssueSource{}, msg
	}
	mapping, msg := normalizeJiraMapping(req.Jira.Mapping)
	if msg != "" {
		return store.IssueSource{}, msg
	}
	return store.IssueSource{
		Provider: provider, JiraSite: site, JiraProjectKey: key, JiraJQL: jql, JiraMappingJSON: mapping,
	}, ""
}

// normalizeJiraSite accepts an absolute https:// site base, or http:// on a
// loopback host only (the fake Jira the gates run against). It refuses
// userinfo — no field of a binding may hold a secret — and any path, query
// or fragment, and returns the site with a lower-cased host and no trailing
// slash.
func normalizeJiraSite(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	const field = "issue_source.jira.site"
	switch {
	case raw == "":
		return "", field + " is required with provider jira"
	case len(raw) > maxJiraSite:
		return "", fmt.Sprintf("%s is longer than %d characters", field, maxJiraSite)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Sprintf("%s %q is not a URL: %v", field, raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme == "" || u.Host == "":
		return "", fmt.Sprintf("%s %q must be an absolute https:// URL", field, raw)
	case u.User != nil:
		return "", fmt.Sprintf("%s must not carry a user or password: the credential comes from JIRA_USER and JIRA_API_TOKEN", field)
	case u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "?"):
		return "", fmt.Sprintf("%s %q must not have a query", field, raw)
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return "", fmt.Sprintf("%s %q must not have a fragment", field, raw)
	case u.Opaque != "" || (u.Path != "" && u.Path != "/"):
		return "", fmt.Sprintf("%s %q must be the site's base URL, with no path", field, raw)
	}
	host := strings.ToLower(u.Hostname())
	switch scheme {
	case "https":
	case "http":
		if !loopbackHost(host) {
			return "", fmt.Sprintf("%s %q must use https://; http:// is allowed only on a loopback host", field, raw)
		}
	default:
		return "", fmt.Sprintf("%s %q must be an absolute https:// URL", field, raw)
	}
	return scheme + "://" + strings.ToLower(u.Host), ""
}

// loopbackHost is localhost or a loopback IP (127.0.0.0/8, ::1).
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateJiraJQL checks the optional narrowing clause. The client ANDs it
// after `project = <id>` (#908), so it may not reorder the result or reach
// past the project; a single line with no ORDER BY is all that can be
// checked without a site.
func validateJiraJQL(raw string) (string, string) {
	jql := strings.TrimSpace(raw)
	const field = "issue_source.jira.jql"
	switch {
	case jql == "":
		return "", ""
	case !utf8.ValidString(jql):
		return "", field + " is not valid UTF-8"
	case len(jql) > maxJiraJQL:
		return "", fmt.Sprintf("%s is longer than %d characters", field, maxJiraJQL)
	case strings.ContainsAny(jql, "\r\n"):
		return "", field + " must be a single line"
	case orderByPattern.MatchString(jql):
		return "", field + " must not contain ORDER BY: it narrows the project's issues, and the sync orders them itself"
	}
	return jql, ""
}

// normalizeJiraMapping checks a mapping override parses strictly into a
// jiramap.Config and returns it compacted, "" for none.
func normalizeJiraMapping(raw json.RawMessage) (string, string) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return "", ""
	}
	if _, err := jiramap.ParseConfig(raw); err != nil {
		return "", fmt.Sprintf("issue_source.jira.%v", err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", fmt.Sprintf("issue_source.jira.mapping: %v", err)
	}
	if buf.String() == "{}" {
		return "", ""
	}
	return buf.String(), ""
}

// effectiveIssueProvider resolves a project's binding against its origin
// (a local git call, never a network one), always afresh — a rebind is
// decided from it — and refreshes the renderings' cache with what it read.
func (s *Server) effectiveIssueProvider(ctx context.Context, p *store.Project) string {
	if p.IssueSource.Provider != store.IssueProviderDerive {
		return p.IssueSource.Provider
	}
	_, ok := s.githubRepo(ctx, p)
	s.origins.put(p.Path, ok)
	return p.IssueSource.Effective(ok)
}

// issueSourceRebound reports whether an update rebinds the project (task
// 142.6 decision 4): its effective provider changed, or its Jira site or
// project key did. A change to only the JQL or the mapping is not a rebind;
// the sweep handles issues that leave the narrowed set.
func issueSourceRebound(oldEff, newEff string, oldB, newB store.IssueSource) bool {
	return oldEff != newEff || oldB.JiraSite != newB.JiraSite || oldB.JiraProjectKey != newB.JiraProjectKey
}

// originTTL is how long a project rendering may reuse what origin said
// (review F2). A listing renders every project, and the TUI board lists them
// on every debounced refresh, so resolving derive afresh each time cost one
// git process per project per event. A re-pointed origin shows up in a
// listing within this long; a rebind is never decided from the cache.
const originTTL = 10 * time.Second

// originCache remembers, per project path, whether origin was a github.com
// remote, for renderings only.
type originCache struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]originEntry
}

type originEntry struct {
	github bool
	at     time.Time
}

func newOriginCache() *originCache {
	return &originCache{now: time.Now, m: map[string]originEntry{}}
}

func (c *originCache) get(path string) (github, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[path]
	if !ok || c.now().Sub(e.at) >= originTTL {
		return false, false
	}
	return e.github, true
}

func (c *originCache) put(path string, github bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[path] = originEntry{github: github, at: c.now()}
}

// renderedIssueProvider is effectiveIssueProvider for a rendering: it reuses
// an origin read within originTTL instead of running git again.
func (s *Server) renderedIssueProvider(ctx context.Context, p *store.Project) string {
	if p.IssueSource.Provider != store.IssueProviderDerive {
		return p.IssueSource.Provider
	}
	if gh, ok := s.origins.get(p.Path); ok {
		return p.IssueSource.Effective(gh)
	}
	return s.effectiveIssueProvider(ctx, p)
}
