package fakejira

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// apiPrefix is Jira Cloud's REST v3 root; every Jira route sits under it.
const apiPrefix = "/rest/api/3/"

// ShutdownPath is the control route that stops cmd/fakejira on every OS.
const ShutdownPath = "/_fake/shutdown"

// notFoundMessage is Jira Cloud's answer for an issue that does not exist and
// for one the caller may not see alike — which is why deleted and forbidden
// are byte-identical from outside.
const notFoundMessage = "Issue does not exist or you do not have permission to see it."

// The scenarios (see doc.go).
const (
	ScenarioRateLimited  = "rate-limited"
	ScenarioUnauthorized = "unauthorized"
	ScenarioReadOnly     = "read-only"
	ScenarioUnreachable  = "unreachable"
	ScenarioSearchLag    = "search-lag"
	// ScenarioResolutionOverridden plays the default company-managed
	// workflow's `system:update-field` post-function (task 142.22, #953):
	// every transition into a done-category status still answers 204, then
	// ends with resolution Done whatever its screen set.
	ScenarioResolutionOverridden = "resolution-overridden"
)

// The resolution ScenarioResolutionOverridden's post-function sets: the
// simplified workflow's Done, id 10000, as on the captured site.
const (
	overriddenResolutionID   = "10000"
	overriddenResolutionName = "Done"
)

// scenario is the parsed scenario for one request.
type scenario struct {
	name string
	lag  int
}

// scenario reads ScenarioFile: its first non-blank line, trimmed. Missing or
// empty, no scenario; `search-lag N` carries N.
func (s *Store) scenario() (scenario, error) {
	if s.ScenarioFile == "" {
		return scenario{}, nil
	}
	raw, err := os.ReadFile(s.ScenarioFile)
	if err != nil {
		return scenario{}, nil //nolint:nilerr // a missing scenario file is no scenario
	}
	line := strings.TrimSpace(string(raw))
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return scenario{}, nil
	}
	switch fields[0] {
	case ScenarioRateLimited, ScenarioUnauthorized, ScenarioReadOnly, ScenarioUnreachable,
		ScenarioResolutionOverridden, "success", "":
		if len(fields) == 1 {
			return scenario{name: fields[0]}, nil
		}
	case ScenarioSearchLag:
		if len(fields) == 2 {
			if n, err := strconv.Atoi(fields[1]); err == nil && n >= 0 {
				return scenario{name: ScenarioSearchLag, lag: n}, nil
			}
		}
	}
	return scenario{}, fmt.Errorf("fakejira: unknown scenario %q", line)
}

// statusRecorder remembers the status a handler answered, for the request log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// Unwrap lets http.ResponseController reach the connection's writer, so a
// Flush through the recorder reaches the client (review F16).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// ServeHTTP answers one request on either leg. It holds the Store's locks
// for the whole request — its own mutex and the corpus lock file — so a
// load-modify-save never interleaves with another, in this process or in a
// test mutating the same corpus file.
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w}
	var hijacked bool
	if err := s.exclusive(func() error {
		hijacked = s.serve(rec, r)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status := rec.status
	if hijacked {
		status = 0
	}
	if err := s.logRequest(r.Method, r.URL.Path, status); err != nil {
		fmt.Fprintln(os.Stderr, "fakejira: request log:", err)
	}
}

// serve answers r, reporting whether the connection was hijacked instead.
func (s *Store) serve(w *statusRecorder, r *http.Request) bool {
	if r.URL.Path == ShutdownPath {
		s.serveShutdown(w, r)
		return false
	}
	sc, err := s.scenario()
	if err != nil {
		writeErrors(w, http.StatusInternalServerError, []string{err.Error()}, nil)
		return false
	}
	switch sc.name {
	case ScenarioUnreachable:
		return hijackClose(w)
	case ScenarioRateLimited:
		// Jira Cloud's rate limiting: 429, Retry-After in seconds, and the
		// reason header (developer.atlassian.com, "Rate limiting").
		w.Header().Set("Retry-After", "2")
		w.Header().Set("X-RateLimit-Reason", "jira-burst-based")
		writeErrors(w, http.StatusTooManyRequests, []string{"Rate limit exceeded."}, nil)
		return false
	case ScenarioUnauthorized:
		writeUnauthorized(w)
		return false
	}
	if !s.authorized(r) {
		writeUnauthorized(w)
		return false
	}
	if !strings.HasPrefix(r.URL.Path, apiPrefix) {
		writeErrors(w, http.StatusNotFound, []string{"No route."}, nil)
		return false
	}
	c, err := s.Load()
	if err != nil {
		writeErrors(w, http.StatusInternalServerError, []string{err.Error()}, nil)
		return false
	}
	h := handler{s: s, c: &c, w: w, r: r, sc: sc, base: origin(r)}
	if h.route(strings.TrimPrefix(r.URL.Path, apiPrefix)) {
		if err := s.Save(c); err != nil {
			fmt.Fprintln(os.Stderr, "fakejira: save corpus:", err)
		}
	}
	return false
}

func (s *Store) serveShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrors(w, http.StatusMethodNotAllowed, []string{"POST only."}, nil)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || s.OnShutdown == nil {
		writeErrors(w, http.StatusNotFound, []string{"No route."}, nil)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	// The 202 goes out before OnShutdown starts, as its doc promises; the
	// controller unwraps the statusRecorder to reach the real writer.
	_ = http.NewResponseController(w).Flush()
	go s.OnShutdown()
}

// hijackClose drops the connection with no answer at all. A writer that
// cannot hijack (HTTP/2) is answered 502 instead, the nearest thing.
func hijackClose(w *statusRecorder) bool {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		writeErrors(w, http.StatusBadGateway, []string{"unreachable"}, nil)
		return false
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		writeErrors(w, http.StatusBadGateway, []string{"unreachable"}, nil)
		return false
	}
	_ = conn.Close()
	return true
}

func (s *Store) authorized(r *http.Request) bool {
	user, token, ok := r.BasicAuth()
	if !ok {
		return false
	}
	// Each credential defaults on its own (review F17): setting only the
	// user must not leave the empty string as the accepted token.
	wantUser, wantToken := s.User, s.Token
	if wantUser == "" {
		wantUser = DefaultUser
	}
	if wantToken == "" {
		wantToken = DefaultToken
	}
	u := subtle.ConstantTimeCompare([]byte(user), []byte(wantUser))
	t := subtle.ConstantTimeCompare([]byte(token), []byte(wantToken))
	return u&t == 1
}

func origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// writeErrors writes Jira's error envelope: `{"errorMessages": [...],
// "errors": {...}}` (REST v3, every 4xx).
func writeErrors(w http.ResponseWriter, status int, messages []string, fields map[string]string) {
	if messages == nil {
		messages = []string{}
	}
	if fields == nil {
		fields = map[string]string{}
	}
	writeJSON(w, status, map[string]any{"errorMessages": messages, "errors": fields})
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="protected-area"`)
	writeErrors(w, http.StatusUnauthorized, []string{"You are not authenticated. Authentication required to perform this operation."}, nil)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handler is one request against one loaded corpus.
type handler struct {
	s    *Store
	c    *Corpus
	w    http.ResponseWriter
	r    *http.Request
	sc   scenario
	base string
}

// route dispatches, reporting whether the corpus changed and must be saved.
func (h *handler) route(path string) bool {
	seg := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(seg) == 1 && seg[0] == "myself":
		return h.get(h.myself)
	case len(seg) == 1 && seg[0] == "serverInfo":
		return h.get(h.serverInfo)
	case len(seg) == 2 && seg[0] == "search" && seg[1] == "jql":
		return h.get(h.search)
	case len(seg) == 1 && seg[0] == "search":
		return h.retiredSearch()
	case len(seg) == 2 && seg[0] == "project":
		return h.get(func() bool { return h.project(seg[1]) })
	case len(seg) == 2 && seg[0] == "issue" && seg[1] == "bulkfetch":
		if h.r.Method != http.MethodPost {
			return h.methodNotAllowed()
		}
		return h.bulkfetch()
	case len(seg) == 2 && seg[0] == "issue":
		return h.get(func() bool { return h.issue(seg[1]) })
	case len(seg) == 3 && seg[0] == "issue" && seg[2] == "transitions":
		switch h.r.Method {
		case http.MethodGet:
			return h.transitions(seg[1])
		case http.MethodPost:
			return h.transition(seg[1])
		}
		return h.methodNotAllowed()
	case len(seg) == 3 && seg[0] == "issue" && seg[2] == "comment":
		switch h.r.Method {
		case http.MethodGet:
			return h.comments(seg[1])
		case http.MethodPost:
			return h.addComment(seg[1])
		}
		return h.methodNotAllowed()
	}
	writeErrors(h.w, http.StatusNotFound, []string{"No route."}, nil)
	return false
}

func (h *handler) get(fn func() bool) bool {
	if h.r.Method != http.MethodGet {
		return h.methodNotAllowed()
	}
	return fn()
}

func (h *handler) methodNotAllowed() bool {
	writeErrors(h.w, http.StatusMethodNotAllowed, []string{"Method not allowed."}, nil)
	return false
}

func (h *handler) notFound() bool {
	writeErrors(h.w, http.StatusNotFound, []string{notFoundMessage}, nil)
	return false
}

// visible finds an issue a caller may see: by id, key or a key it had before
// a move, and neither deleted nor forbidden.
func (h *handler) visible(idOrKey string) *Issue {
	is := h.c.issue(idOrKey)
	if is == nil || is.hidden() {
		return nil
	}
	return is
}

// myself is GET /rest/api/3/myself (REST v3 "Get current user").
func (h *handler) myself() bool {
	u := h.c.Site.Myself
	writeJSON(h.w, http.StatusOK, map[string]any{
		"self":         h.base + apiPrefix + "user?accountId=" + u.AccountID,
		"accountId":    u.AccountID,
		"accountType":  "atlassian",
		"emailAddress": u.EmailAddress,
		"displayName":  u.DisplayName,
		"active":       true,
		"timeZone":     "UTC",
		"locale":       "en_US",
	})
	return false
}

// serverInfo is GET /rest/api/3/serverInfo (REST v3 "Get Jira instance info").
func (h *handler) serverInfo() bool {
	base := h.c.Site.BaseURL
	if base == "" {
		base = h.base
	}
	title := h.c.Site.ServerTitle
	if title == "" {
		title = "Jira"
	}
	writeJSON(h.w, http.StatusOK, map[string]any{
		"baseUrl":        base,
		"displayUrl":     base,
		"version":        "1001.0.0-SNAPSHOT",
		"versionNumbers": []int{1001, 0, 0},
		"deploymentType": "Cloud",
		"buildNumber":    100000,
		"buildDate":      "2026-01-01T00:00:00.000+0000",
		"serverTime":     h.s.stamp(),
		"scmInfo":        "fakejira",
		"serverTitle":    title,
	})
	return false
}

// retiredSearch is /rest/api/3/search, which Atlassian removed in favour of
// search/jql (CHANGE-2046) and now answers 410 Gone. The fake answers the
// same, so a client that strays onto it fails loudly — and the request log
// records the stray, for a test asserting none happened.
func (h *handler) retiredSearch() bool {
	writeErrors(h.w, http.StatusGone, []string{"The requested API has been removed. Please migrate to the /rest/api/3/search/jql API."}, nil)
	return false
}

// project is GET /rest/api/3/project/{projectIdOrKey} (REST v3 "Get
// project"): the project by key or id, or 404 with Jira's errorMessages.
func (h *handler) project(idOrKey string) bool {
	var p *Project
	for i := range h.c.Projects {
		if h.c.Projects[i].Key == idOrKey || h.c.Projects[i].ID == idOrKey {
			p = &h.c.Projects[i]
			break
		}
	}
	if p == nil {
		writeErrors(h.w, http.StatusNotFound, []string{fmt.Sprintf("No project could be found with key '%s'.", idOrKey)}, nil)
		return false
	}
	writeJSON(h.w, http.StatusOK, map[string]any{
		"self":           h.base + apiPrefix + "project/" + p.ID,
		"id":             p.ID,
		"key":            p.Key,
		"name":           p.Name,
		"description":    "",
		"projectTypeKey": "software",
		"simplified":     false,
		"style":          "classic",
		"isPrivate":      false,
		"expand":         "description,lead,issueTypes,url,projectKeys,permissions,insight",
	})
	return false
}

// pageToken encodes an offset as an opaque nextPageToken.
func pageToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(offset)))
}

func parsePageToken(tok string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || !strings.HasPrefix(string(raw), "o:") {
		return 0, errors.New("invalid nextPageToken")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(raw), "o:"))
	if err != nil || n < 0 {
		return 0, errors.New("invalid nextPageToken")
	}
	return n, nil
}

func maxResults(q string, def int) (int, error) {
	if q == "" {
		return def, nil
	}
	n, err := strconv.Atoi(q)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("maxResults must be a non-negative integer")
	}
	if n == 0 {
		return def, nil
	}
	return min(n, 5000), nil
}

// search is GET /rest/api/3/search/jql (REST v3 "Search for issues using JQL
// enhanced search"): `issues`, `nextPageToken` while more remain, `isLast`,
// and — unlike the retired /search — no `total`. Its default fields are
// `id` alone, so with no `fields` an issue carries no `fields` object.
func (h *handler) search() bool {
	qv := h.r.URL.Query()
	q, err := ParseJQL(qv.Get("jql"))
	if err != nil {
		writeErrors(h.w, http.StatusBadRequest, []string{"Error in the JQL Query: " + err.Error()}, nil)
		return false
	}
	limit, err := maxResults(qv.Get("maxResults"), 50)
	if err != nil {
		writeErrors(h.w, http.StatusBadRequest, []string{err.Error()}, nil)
		return false
	}
	offset := 0
	if tok := qv.Get("nextPageToken"); tok != "" {
		if offset, err = parsePageToken(tok); err != nil {
			writeErrors(h.w, http.StatusBadRequest, []string{err.Error()}, nil)
			return false
		}
	}
	fs := parseFields(qv["fields"], false)
	rendered := hasExpand(qv["expand"], "renderedFields")

	changed := false
	lagging := h.c.Fake.SearchLag > 0
	var view []Issue
	for _, is := range h.c.Issues {
		if is.hidden() {
			continue
		}
		if lagging && is.Fake != nil && is.Fake.Stale != nil {
			is.Status, is.Updated, is.Resolution = is.Fake.Stale.Status, is.Fake.Stale.Updated, is.Fake.Stale.Resolution
		}
		if q.match(h.c, &is) {
			view = append(view, is)
		}
	}
	if lagging {
		h.c.Fake.SearchLag--
		if h.c.Fake.SearchLag == 0 {
			for i := range h.c.Issues {
				if h.c.Issues[i].Fake != nil {
					h.c.Issues[i].Fake.Stale = nil
				}
			}
		}
		changed = true
	}
	q.sort(view)
	end := min(offset+limit, len(view))
	start := min(offset, end)
	out := make([]map[string]any, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, h.render(&view[i], fs, rendered))
	}
	body := map[string]any{"issues": out, "isLast": end >= len(view)}
	if end < len(view) {
		body["nextPageToken"] = pageToken(end)
	}
	writeJSON(h.w, http.StatusOK, body)
	return changed
}

// bulkfetch is POST /rest/api/3/issue/bulkfetch (REST v3 "Bulk fetch
// issues"): `{"issueIdsOrKeys", "fields", "expand"}` in, `{"issues",
// "issueErrors"}` out. A deleted id and a forbidden one are both simply
// omitted, so the two cannot be told apart.
func (h *handler) bulkfetch() bool {
	var req struct {
		IssueIDsOrKeys []string `json:"issueIdsOrKeys"`
		Fields         []string `json:"fields"`
		Expand         []string `json:"expand"`
	}
	if err := json.NewDecoder(h.r.Body).Decode(&req); err != nil {
		writeErrors(h.w, http.StatusBadRequest, []string{"Invalid request payload."}, nil)
		return false
	}
	fs := parseFields(req.Fields, true)
	rendered := hasExpand(req.Expand, "renderedFields")
	out := []map[string]any{}
	for _, k := range req.IssueIDsOrKeys {
		if is := h.visible(k); is != nil {
			out = append(out, h.render(is, fs, rendered))
		}
	}
	writeJSON(h.w, http.StatusOK, map[string]any{"issues": out, "issueErrors": []any{}})
	return false
}

// issue is GET /rest/api/3/issue/{idOrKey} (REST v3 "Get issue"). A key the
// issue had before a move resolves to it, as Jira's does.
func (h *handler) issue(idOrKey string) bool {
	is := h.visible(idOrKey)
	if is == nil {
		return h.notFound()
	}
	qv := h.r.URL.Query()
	writeJSON(h.w, http.StatusOK, h.render(is, parseFields(qv["fields"], true), hasExpand(qv["expand"], "renderedFields")))
	return false
}

// transitions is GET /rest/api/3/issue/{k}/transitions (REST v3 "Get
// transitions"): only what the current status allows; the screen's fields
// only with expand=transitions.fields.
func (h *handler) transitions(idOrKey string) bool {
	is := h.visible(idOrKey)
	if is == nil {
		return h.notFound()
	}
	p := h.c.project(is.Project)
	withFields := hasExpand(h.r.URL.Query()["expand"], "transitions.fields")
	out := []map[string]any{}
	if p != nil {
		for _, t := range p.available(is.Status) {
			tr := map[string]any{
				"id":            t.ID,
				"name":          t.Name,
				"to":            h.renderStatus(p, t.To),
				"hasScreen":     len(t.Fields) > 0,
				"isGlobal":      len(t.From) == 0,
				"isInitial":     false,
				"isAvailable":   true,
				"isConditional": false,
				"isLooped":      false,
			}
			if withFields {
				fields := map[string]any{}
				for _, f := range t.Fields {
					fd := map[string]any{
						"required":        f.Required,
						"name":            f.Name,
						"key":             f.Key,
						"hasDefaultValue": false,
						"operations":      []string{"set"},
						"schema":          map[string]any{"type": f.Key, "system": f.Key},
					}
					if len(f.AllowedValues) > 0 {
						av := make([]map[string]any, 0, len(f.AllowedValues))
						for _, a := range f.AllowedValues {
							av = append(av, map[string]any{"self": h.base + apiPrefix + f.Key + "/" + a.ID, "id": a.ID, "name": a.Name})
						}
						fd["allowedValues"] = av
					}
					fields[f.Key] = fd
				}
				tr["fields"] = fields
			}
			out = append(out, tr)
		}
	}
	writeJSON(h.w, http.StatusOK, map[string]any{"expand": "transitions", "transitions": out})
	return false
}

// transition is POST /rest/api/3/issue/{k}/transitions (REST v3 "Transition
// issue"): `{"transition": {"id"}, "fields": {...}}` in, 204 out, or 400 with
// Jira's `errors` map for the screen.
func (h *handler) transition(idOrKey string) bool {
	is := h.visible(idOrKey)
	if is == nil {
		return h.notFound()
	}
	if h.sc.name == ScenarioReadOnly {
		writeErrors(h.w, http.StatusForbidden, []string{"You do not have permission to transition this issue."}, nil)
		return false
	}
	var req struct {
		Transition struct {
			ID string `json:"id"`
		} `json:"transition"`
		Fields map[string]any `json:"fields"`
	}
	if err := json.NewDecoder(h.r.Body).Decode(&req); err != nil {
		writeErrors(h.w, http.StatusBadRequest, []string{"Invalid request payload."}, nil)
		return false
	}
	before := Stale{Status: is.Status, Updated: is.Updated, Resolution: is.Resolution}
	if err := h.c.applyTransition(is, req.Transition.ID, req.Fields, h.s.stamp()); err != nil {
		var fe *FieldErrors
		if errors.As(err, &fe) {
			writeErrors(h.w, http.StatusBadRequest, fe.Messages, fe.Fields)
			return false
		}
		writeErrors(h.w, http.StatusInternalServerError, []string{err.Error()}, nil)
		return false
	}
	if h.sc.name == ScenarioResolutionOverridden {
		if p := h.c.project(is.Project); p != nil {
			if st := p.status(is.Status); st != nil && st.Category == CategoryDone {
				is.Resolution = &Allowed{ID: overriddenResolutionID, Name: overriddenResolutionName}
			}
		}
	}
	h.armLag(is, before)
	h.w.WriteHeader(http.StatusNoContent)
	return true
}

// armLag records the pre-write view search-lag answers, after a REST write.
func (h *handler) armLag(is *Issue, before Stale) {
	if h.sc.name != ScenarioSearchLag || h.sc.lag == 0 {
		return
	}
	if is.Fake == nil {
		is.Fake = &IssueFake{}
	}
	if is.Fake.Stale == nil {
		is.Fake.Stale = &before
	}
	h.c.Fake.SearchLag = h.sc.lag
}

// comments is GET /rest/api/3/issue/{k}/comment (REST v3 "Get comments"):
// a page with startAt, maxResults and total, oldest first; renderedBody
// only with expand=renderedBody.
func (h *handler) comments(idOrKey string) bool {
	is := h.visible(idOrKey)
	if is == nil {
		return h.notFound()
	}
	qv := h.r.URL.Query()
	startAt := 0
	if v := qv.Get("startAt"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErrors(h.w, http.StatusBadRequest, []string{"startAt must be a non-negative integer"}, nil)
			return false
		}
		startAt = n
	}
	limit, err := maxResults(qv.Get("maxResults"), 50)
	if err != nil {
		writeErrors(h.w, http.StatusBadRequest, []string{err.Error()}, nil)
		return false
	}
	rendered := hasExpand(qv["expand"], "renderedBody")
	var thread []Comment
	for _, cm := range h.c.Comments {
		if cm.Issue == is.ID {
			thread = append(thread, cm)
		}
	}
	end := min(startAt+limit, len(thread))
	start := min(startAt, end)
	out := make([]map[string]any, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, h.renderComment(is, &thread[i], rendered))
	}
	writeJSON(h.w, http.StatusOK, map[string]any{
		"startAt": startAt, "maxResults": limit, "total": len(thread), "comments": out,
	})
	return false
}

// addComment is POST /rest/api/3/issue/{k}/comment (REST v3 "Add comment"):
// `{"body": ADF, "visibility"?}` in, 201 with the comment out.
func (h *handler) addComment(idOrKey string) bool {
	is := h.visible(idOrKey)
	if is == nil {
		return h.notFound()
	}
	if h.sc.name == ScenarioReadOnly {
		writeErrors(h.w, http.StatusForbidden, []string{"You do not have the permission to comment on this issue."}, nil)
		return false
	}
	var req struct {
		Body       json.RawMessage `json:"body"`
		Visibility *Visibility     `json:"visibility"`
	}
	raw, err := io.ReadAll(h.r.Body)
	if err == nil {
		err = json.Unmarshal(raw, &req)
	}
	if err != nil || len(req.Body) == 0 {
		writeErrors(h.w, http.StatusBadRequest, nil, map[string]string{"comment": "Comment body can not be empty!"})
		return false
	}
	html, err := RenderADF(req.Body)
	if err != nil {
		writeErrors(h.w, http.StatusBadRequest, []string{"INVALID_INPUT"}, map[string]string{"comment": err.Error()})
		return false
	}
	before := Stale{Status: is.Status, Updated: is.Updated, Resolution: is.Resolution}
	now := h.s.stamp()
	cm := Comment{
		ID: h.c.nextCommentID(), Issue: is.ID, Author: h.c.Site.Myself,
		Body: req.Body, BodyHTML: html, Visibility: req.Visibility, Created: now, Updated: now,
	}
	h.c.Comments = append(h.c.Comments, cm)
	is.Updated = now
	h.armLag(is, before)
	writeJSON(h.w, http.StatusCreated, h.renderComment(is, &cm, hasExpand(h.r.URL.Query()["expand"], "renderedBody")))
	return true
}
