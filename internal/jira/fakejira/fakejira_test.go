package fakejira

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/testutil/wait"
)

type fixture struct {
	t     *testing.T
	store *Store
	srv   *httptest.Server
	user  string
	token string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	clock := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	store := &Store{
		Path:         filepath.Join(dir, "corpus.json"),
		ScenarioFile: filepath.Join(dir, "scenario"),
		RequestLog:   filepath.Join(dir, "requests.log"),
		Now:          func() time.Time { return clock },
	}
	srv := httptest.NewServer(store)
	t.Cleanup(srv.Close)
	return &fixture{t: t, store: store, srv: srv, user: DefaultUser, token: DefaultToken}
}

type answer struct {
	status int
	header http.Header
	raw    []byte
	body   map[string]any
}

func (f *fixture) do(method, path string, body any) answer {
	f.t.Helper()
	return f.doAuth(method, path, body, true)
}

func (f *fixture) doAuth(method, path string, body any, auth bool) answer {
	f.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	if auth {
		req.SetBasicAuth(f.user, f.token)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	a := answer{status: resp.StatusCode, header: resp.Header, raw: raw}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &a.body); err != nil {
			f.t.Fatalf("%s %s: %d %s: %v", method, path, resp.StatusCode, raw, err)
		}
	}
	return a
}

func (f *fixture) want(a answer, status int) answer {
	f.t.Helper()
	if a.status != status {
		f.t.Fatalf("status %d, want %d: %s", a.status, status, a.raw)
	}
	return a
}

func (f *fixture) scenario(s string) {
	f.t.Helper()
	if err := os.WriteFile(f.store.ScenarioFile, []byte(s+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func search(jql string, extra ...string) string {
	q := url.Values{"jql": {jql}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Add(extra[i], extra[i+1])
	}
	return "/rest/api/3/search/jql?" + q.Encode()
}

func keys(t *testing.T, a answer) []string {
	t.Helper()
	issues, _ := a.body["issues"].([]any)
	out := []string{}
	for _, is := range issues {
		out = append(out, is.(map[string]any)["key"].(string))
	}
	return out
}

func path(v any, p ...string) any {
	for _, k := range p {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func TestSearchPagesWithoutTotal(t *testing.T) {
	f := newFixture(t)
	var got []string
	tok := ""
	for page := 0; ; page++ {
		if page > 3 {
			t.Fatal("search never reached isLast")
		}
		extra := []string{"maxResults", "1"}
		if tok != "" {
			extra = append(extra, "nextPageToken", tok)
		}
		a := f.want(f.do("GET", search(`project = "VIN" ORDER BY id ASC`, extra...), nil), 200)
		if _, ok := a.body["total"]; ok {
			t.Fatalf("search/jql answered a total: %s", a.raw)
		}
		if is := a.body["issues"].([]any); len(is) != 1 {
			t.Fatalf("page %d: %d issues", page, len(is))
		} else if _, ok := is[0].(map[string]any)["fields"]; ok {
			t.Fatalf("no fields asked, fields answered: %s", a.raw)
		}
		got = append(got, keys(t, a)...)
		last := a.body["isLast"].(bool)
		next, hasNext := a.body["nextPageToken"].(string)
		if last == hasNext {
			t.Fatalf("page %d: isLast %v with nextPageToken %q", page, last, next)
		}
		if last {
			if page != 2 {
				t.Fatalf("isLast on page %d, want 2", page)
			}
			break
		}
		tok = next
	}
	if want := []string{"VIN-1", "VIN-2", "VIN-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pages %v, want %v", got, want)
	}
}

func TestSearchJQLSubset(t *testing.T) {
	f := newFixture(t)
	since := time.Date(2026, 8, 2, 10, 0, 0, 123e6, time.UTC).UnixMilli()
	for _, tc := range []struct {
		jql  string
		want []string
	}{
		{`project = 10000`, []string{"VIN-1", "VIN-2", "VIN-3"}},
		{`project = "OPS"`, []string{}},
		{`project = "VIN" AND updated >= ` + strconv.FormatInt(since, 10), []string{"VIN-2", "VIN-3"}},
		{`project = "VIN" AND updated >= ` + strconv.FormatInt(since+1, 10), []string{"VIN-3"}},
		{`project = "VIN" and statusCategory != Done`, []string{"VIN-1", "VIN-2"}},
		{`project = "VIN" AND id > 10001 ORDER BY id ASC`, []string{"VIN-2", "VIN-3"}},
	} {
		a := f.want(f.do("GET", search(tc.jql), nil), 200)
		if got := keys(t, a); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.jql, got, tc.want)
		}
	}

	// ORDER BY updated ASC, id ASC: VIN-1 moves last, and a tie on updated
	// falls back to the id.
	if err := f.store.SetUpdated("VIN-1", time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	a := f.want(f.do("GET", search(`project = "VIN" ORDER BY updated ASC, id ASC`), nil), 200)
	if got, want := keys(t, a), []string{"VIN-2", "VIN-1", "VIN-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("updated order %v, want %v", got, want)
	}
	a = f.want(f.do("GET", search(`project = "VIN" ORDER BY id ASC`), nil), 200)
	if got, want := keys(t, a), []string{"VIN-1", "VIN-2", "VIN-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("id order %v, want %v", got, want)
	}

	for _, jql := range []string{
		``,
		`project = VIN`,
		`summary ~ "sync"`,
		`project = "VIN" OR id > 1`,
		`(project = "VIN")`,
		`project = "VIN" ORDER BY created DESC`,
		`project = "VIN" ORDER BY updated DESC`,
		`statusCategory = Done`,
		`updated >= "2026-08-01"`,
	} {
		a := f.do("GET", search(jql), nil)
		if a.status != 400 || len(a.body["errorMessages"].([]any)) == 0 {
			t.Errorf("%q: %d %s, want a 400 with errorMessages", jql, a.status, a.raw)
		}
	}
}

func TestFieldsAndRenderedFields(t *testing.T) {
	f := newFixture(t)
	a := f.want(f.do("GET", search(`project = "VIN"`, "fields", "summary,status", "maxResults", "1"), nil), 200)
	is := a.body["issues"].([]any)[0].(map[string]any)
	fields := is["fields"].(map[string]any)
	if len(fields) != 2 || fields["summary"] != "Sync Jira issues into vincent" {
		t.Fatalf("fields %v, want summary and status alone", fields)
	}
	if got := path(fields, "status", "statusCategory", "key"); got != CategoryNew {
		t.Fatalf("statusCategory key %v", got)
	}
	if _, ok := is["renderedFields"]; ok {
		t.Fatal("renderedFields answered unasked")
	}

	a = f.want(f.do("GET", search(`project = "VIN"`, "fields", "description", "expand", "renderedFields", "maxResults", "1"), nil), 200)
	is = a.body["issues"].([]any)[0].(map[string]any)
	if got := path(is, "renderedFields", "description"); got != "<p>Mirror the board.</p>" {
		t.Fatalf("rendered description %v", got)
	}
	if got := path(is, "fields", "description", "type"); got != "doc" {
		t.Fatalf("ADF description %v", got)
	}

	a = f.want(f.do("GET", "/rest/api/3/issue/VIN-3", nil), 200)
	for _, k := range navigable {
		if _, ok := a.body["fields"].(map[string]any)[k]; !ok {
			t.Errorf("GET issue lacks field %s", k)
		}
	}
	if got := path(a.body, "fields", "resolution", "name"); got != "Done" {
		t.Fatalf("resolution %v", got)
	}
}

func TestMovedIssueKeepsIDAndOldKey(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Move("VIN-1", "OPS", "OPS-7"); err != nil {
		t.Fatal(err)
	}
	a := f.want(f.do("GET", "/rest/api/3/issue/VIN-1", nil), 200)
	if a.body["id"] != "10001" || a.body["key"] != "OPS-7" || path(a.body, "fields", "project", "key") != "OPS" {
		t.Fatalf("old key answered %v %v", a.body["id"], a.body["key"])
	}
	if got := keys(t, f.want(f.do("GET", search(`project = "OPS"`), nil), 200)); !reflect.DeepEqual(got, []string{"OPS-7"}) {
		t.Fatalf("OPS search %v", got)
	}
	if got := keys(t, f.want(f.do("GET", search(`project = "VIN"`), nil), 200)); !reflect.DeepEqual(got, []string{"VIN-2", "VIN-3"}) {
		t.Fatalf("VIN search %v", got)
	}
}

func TestDeletedAndForbiddenLookAlike(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Delete("VIN-2"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Forbid("10003"); err != nil {
		t.Fatal(err)
	}
	del := f.want(f.do("GET", "/rest/api/3/issue/VIN-2", nil), 404)
	forb := f.want(f.do("GET", "/rest/api/3/issue/VIN-3", nil), 404)
	if !bytes.Equal(del.raw, forb.raw) || !strings.Contains(string(del.raw), "errorMessages") {
		t.Fatalf("deleted %s, forbidden %s", del.raw, forb.raw)
	}
	for _, p := range []string{"/rest/api/3/issue/VIN-2/comment", "/rest/api/3/issue/VIN-3/transitions"} {
		if a := f.do("GET", p, nil); a.status != 404 || !bytes.Equal(a.raw, del.raw) {
			t.Errorf("%s: %d %s", p, a.status, a.raw)
		}
	}
	a := f.want(f.do("POST", "/rest/api/3/issue/bulkfetch", map[string]any{
		"issueIdsOrKeys": []string{"VIN-1", "VIN-2", "VIN-3", "10003"}, "fields": []string{"summary"},
	}), 200)
	if got := keys(t, a); !reflect.DeepEqual(got, []string{"VIN-1"}) {
		t.Fatalf("bulkfetch %v", got)
	}
	if got := keys(t, f.want(f.do("GET", search(`project = "VIN"`), nil), 200)); !reflect.DeepEqual(got, []string{"VIN-1"}) {
		t.Fatalf("search %v", got)
	}
	if strings.Contains(string(a.raw), "_fake") {
		t.Fatalf("_fake reached an answer: %s", a.raw)
	}
}

func transitionIDs(a answer) []string {
	out := []string{}
	for _, tr := range a.body["transitions"].([]any) {
		out = append(out, tr.(map[string]any)["id"].(string))
	}
	return out
}

func TestTransitions(t *testing.T) {
	f := newFixture(t)
	a := f.want(f.do("GET", "/rest/api/3/issue/VIN-1/transitions?expand=transitions.fields", nil), 200)
	if got := transitionIDs(a); !reflect.DeepEqual(got, []string{"11", "31"}) {
		t.Fatalf("from To Do: %v", got)
	}
	done := a.body["transitions"].([]any)[1].(map[string]any)
	if path(done, "fields", "resolution", "required") != true || len(path(done, "fields", "resolution", "allowedValues").([]any)) != 2 {
		t.Fatalf("Done's screen %v", done["fields"])
	}
	if path(done, "to", "statusCategory", "key") != CategoryDone {
		t.Fatalf("Done's target %v", done["to"])
	}
	plain := f.want(f.do("GET", "/rest/api/3/issue/VIN-1/transitions", nil), 200)
	if _, ok := plain.body["transitions"].([]any)[1].(map[string]any)["fields"]; ok {
		t.Fatal("screen fields answered without expand")
	}

	tp := "/rest/api/3/issue/VIN-1/transitions"
	a = f.want(f.do("POST", tp, map[string]any{"transition": map[string]any{"id": "31"}}), 400)
	if path(a.body, "errors", "resolution") == nil {
		t.Fatalf("missing required field: %s", a.raw)
	}
	a = f.want(f.do("POST", tp, map[string]any{"transition": map[string]any{"id": "31"}, "fields": map[string]any{"resolution": map[string]any{"name": "Bogus"}}}), 400)
	if path(a.body, "errors", "resolution") == nil {
		t.Fatalf("value outside allowedValues: %s", a.raw)
	}
	a = f.want(f.do("POST", tp, map[string]any{"transition": map[string]any{"id": "41"}}), 400)
	if len(a.body["errorMessages"].([]any)) == 0 {
		t.Fatalf("unavailable transition: %s", a.raw)
	}

	f.want(f.do("POST", tp, map[string]any{"transition": map[string]any{"id": "31"}, "fields": map[string]any{"resolution": map[string]any{"name": "Won't Do"}}}), 204)
	a = f.want(f.do("GET", "/rest/api/3/issue/VIN-1", nil), 200)
	if path(a.body, "fields", "status", "statusCategory", "key") != CategoryDone || path(a.body, "fields", "resolution", "name") != "Won't Do" {
		t.Fatalf("after Done: %v %v", path(a.body, "fields", "status"), path(a.body, "fields", "resolution"))
	}
	if path(a.body, "fields", "updated") != "2026-09-01T12:00:00.000+0000" {
		t.Fatalf("updated %v", path(a.body, "fields", "updated"))
	}
	if got := transitionIDs(f.want(f.do("GET", tp, nil), 200)); !reflect.DeepEqual(got, []string{"41"}) {
		t.Fatalf("from Done: %v", got)
	}
	f.want(f.do("POST", tp, map[string]any{"transition": map[string]any{"id": "41"}}), 204)
	a = f.want(f.do("GET", "/rest/api/3/issue/VIN-1", nil), 200)
	if path(a.body, "fields", "status", "statusCategory", "key") != CategoryNew || path(a.body, "fields", "resolution") != nil {
		t.Fatalf("after Reopen: %v %v", path(a.body, "fields", "status"), path(a.body, "fields", "resolution"))
	}
}

func adf(nodes ...map[string]any) map[string]any {
	content := make([]any, len(nodes))
	for i, n := range nodes {
		content[i] = n
	}
	return map[string]any{"type": "doc", "version": 1, "content": []any{map[string]any{"type": "paragraph", "content": content}}}
}

func TestComments(t *testing.T) {
	f := newFixture(t)
	cp := "/rest/api/3/issue/VIN-1/comment"
	a := f.want(f.do("GET", cp+"?startAt=1&maxResults=1", nil), 200)
	cms := a.body["comments"].([]any)
	if a.body["total"] != float64(2) || a.body["startAt"] != float64(1) || len(cms) != 1 {
		t.Fatalf("page %s", a.raw)
	}
	cm := cms[0].(map[string]any)
	if cm["id"] != "20002" || path(cm, "visibility", "value") != "Administrators" {
		t.Fatalf("second comment %v", cm)
	}
	if _, ok := cm["renderedBody"]; ok {
		t.Fatal("renderedBody answered unasked")
	}
	a = f.want(f.do("GET", cp+"?expand=renderedBody", nil), 200)
	if got := path(a.body["comments"].([]any)[1], "renderedBody"); got != "<p>Admins only.</p>" {
		t.Fatalf("renderedBody %v", got)
	}

	body := adf(map[string]any{"type": "text", "text": "a & b"}, map[string]any{"type": "hardBreak"}, map[string]any{"type": "text", "text": "c"})
	a = f.want(f.do("POST", cp, map[string]any{"body": body, "visibility": map[string]any{"type": "role", "value": "Developers"}}), 201)
	if a.body["id"] != "20003" || path(a.body, "visibility", "value") != "Developers" || path(a.body, "author", "accountId") != "557058:fake-vincent" {
		t.Fatalf("POST answered %s", a.raw)
	}
	a = f.want(f.do("GET", cp+"?expand=renderedBody&startAt=2", nil), 200)
	if got := path(a.body["comments"].([]any)[0], "renderedBody"); got != "<p>a &amp; b<br/>c</p>" {
		t.Fatalf("rendered POST %v", got)
	}
	if got := path(f.want(f.do("GET", "/rest/api/3/issue/VIN-1?fields=updated", nil), 200).body, "fields", "updated"); got != "2026-09-01T12:00:00.000+0000" {
		t.Fatalf("a comment did not bump updated: %v", got)
	}

	for name, bad := range map[string]any{
		"heading": map[string]any{"type": "doc", "version": 1, "content": []any{map[string]any{"type": "heading", "content": []any{}}}},
		"mention": adf(map[string]any{"type": "mention"}),
		"marks":   adf(map[string]any{"type": "text", "text": "x", "marks": []any{map[string]any{"type": "strong"}}}),
		"version": map[string]any{"type": "doc", "version": 2, "content": []any{}},
		"string":  "plain text",
	} {
		if a := f.do("POST", cp, map[string]any{"body": bad}); a.status != 400 {
			t.Errorf("%s: %d %s, want 400", name, a.status, a.raw)
		}
	}
	if a := f.do("GET", cp, nil); a.body["total"] != float64(3) {
		t.Fatalf("a rejected POST was kept: %s", a.raw)
	}
}

func TestCommentHelpers(t *testing.T) {
	f := newFixture(t)
	id, err := f.store.AddComment("VIN-2", json.RawMessage(`{"type":"doc","version":1,"content":[]}`), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.EditComment(id, json.RawMessage(`{"type":"doc","version":1,"content":[]}`), "<p>edited</p>"); err != nil {
		t.Fatal(err)
	}
	a := f.want(f.do("GET", "/rest/api/3/issue/VIN-2/comment?expand=renderedBody", nil), 200)
	if a.body["total"] != float64(1) || path(a.body["comments"].([]any)[0], "renderedBody") != "<p>edited</p>" {
		t.Fatalf("after add/edit %s", a.raw)
	}
	if err := f.store.RemoveComment(id); err != nil {
		t.Fatal(err)
	}
	if a := f.do("GET", "/rest/api/3/issue/VIN-2/comment", nil); a.body["total"] != float64(0) {
		t.Fatalf("after remove %s", a.raw)
	}
	if err := (&Store{}).Delete("VIN-1"); !errors.Is(err, ErrNoCorpusFile) {
		t.Fatalf("helper without a path: %v", err)
	}
}

func TestScenarios(t *testing.T) {
	f := newFixture(t)
	issue := "/rest/api/3/issue/VIN-1"

	f.scenario("rate-limited")
	a := f.want(f.do("GET", issue, nil), 429)
	if a.header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}

	f.scenario("unauthorized")
	f.want(f.do("GET", issue, nil), 401)

	f.scenario("read-only")
	f.want(f.do("GET", issue, nil), 200)
	f.want(f.do("POST", issue+"/transitions", map[string]any{"transition": map[string]any{"id": "11"}}), 403)
	f.want(f.do("POST", issue+"/comment", map[string]any{"body": adf(map[string]any{"type": "text", "text": "x"})}), 403)

	f.scenario("unreachable")
	req, _ := http.NewRequest("GET", f.srv.URL+issue, nil)
	req.SetBasicAuth(f.user, f.token)
	if resp, err := f.srv.Client().Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("unreachable answered %d", resp.StatusCode)
	}

	// The same Store, switched back without a restart.
	f.scenario("")
	f.want(f.do("GET", issue, nil), 200)
	f.scenario("nonsense")
	f.want(f.do("GET", issue, nil), 500)
	f.scenario("success")
	f.want(f.do("GET", issue, nil), 200)

	reqs, err := Requests(f.store.RequestLog)
	if err != nil {
		t.Fatal(err)
	}
	// net/http retries an idempotent GET once on a connection closed
	// before any answer, so unreachable may be logged more than once.
	dropped := 0
	for _, r := range reqs {
		if r.Status == 0 {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("unreachable logged no status 0")
	}
	if n, _ := CountRequests(f.store.RequestLog, "GET", "rest/api/3/issue/VIN-1", 0); n != 6+dropped {
		t.Fatalf("logged %d GETs, want %d", n, 6+dropped)
	}
	for status, want := range map[int]int{429: 1, 401: 1, 500: 1, 200: 3} {
		if n, _ := CountRequests(f.store.RequestLog, "GET", "rest/api/3/issue/VIN-1", status); n != want {
			t.Errorf("logged %d GETs answered %d, want %d", n, status, want)
		}
	}
	if n, _ := CountRequests(f.store.RequestLog, "POST", "rest/api/3/issue/VIN-1/comment", 403); n != 1 {
		t.Errorf("logged %d read-only comment POSTs", n)
	}
}

func TestSearchLag(t *testing.T) {
	f := newFixture(t)
	f.scenario("search-lag 2")
	f.want(f.do("POST", "/rest/api/3/issue/VIN-1/transitions", map[string]any{"transition": map[string]any{"id": "11"}}), 204)
	if got := path(f.want(f.do("GET", "/rest/api/3/issue/VIN-1?fields=status", nil), 200).body, "fields", "status", "id"); got != "10001" {
		t.Fatalf("GET issue lags too: %v", got)
	}
	q := search(`project = "VIN" AND id > 10000 ORDER BY id ASC`, "fields", "status,updated", "maxResults", "1")
	for call, want := range []string{"10000", "10000", "10001", "10001"} {
		a := f.want(f.do("GET", q, nil), 200)
		is := a.body["issues"].([]any)[0]
		if got := path(is, "fields", "status", "id"); got != want {
			t.Fatalf("search call %d: status %v, want %v", call+1, got, want)
		}
		wantUpdated := "2026-08-01T09:00:00.000+0000"
		if want == "10001" {
			wantUpdated = "2026-09-01T12:00:00.000+0000"
		}
		if got := path(is, "fields", "updated"); got != wantUpdated {
			t.Fatalf("search call %d: updated %v, want %v", call+1, got, wantUpdated)
		}
		if strings.Contains(string(a.raw), "_fake") || strings.Contains(string(a.raw), "stale") {
			t.Fatalf("_fake reached an answer: %s", a.raw)
		}
	}
}

func TestCorpusEditedOnDisk(t *testing.T) {
	f := newFixture(t)
	f.want(f.do("GET", "/rest/api/3/issue/VIN-1", nil), 200)
	c, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	c.Issues[0].Summary = "Edited by hand"
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(f.store.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := path(f.want(f.do("GET", "/rest/api/3/issue/VIN-1", nil), 200).body, "fields", "summary"); got != "Edited by hand" {
		t.Fatalf("summary %v", got)
	}
}

func TestBasicAuth(t *testing.T) {
	f := newFixture(t)
	f.want(f.doAuth("GET", "/rest/api/3/myself", nil, false), 401)
	f.token = "wrong"
	f.want(f.do("GET", "/rest/api/3/myself", nil), 401)
	f.user, f.token = "someone@example.com", DefaultToken
	f.want(f.do("GET", "/rest/api/3/myself", nil), 401)
	f.user = DefaultUser
	a := f.want(f.do("GET", "/rest/api/3/myself", nil), 200)
	if a.body["accountId"] != "557058:fake-vincent" {
		t.Fatalf("myself %s", a.raw)
	}
	a = f.want(f.do("GET", "/rest/api/3/serverInfo", nil), 200)
	if a.body["deploymentType"] != "Cloud" || a.body["baseUrl"] != f.srv.URL {
		t.Fatalf("serverInfo %s", a.raw)
	}

	f.store.User, f.store.Token = "ada@example.com", "s3cret"
	f.want(f.do("GET", "/rest/api/3/myself", nil), 401)
	f.user, f.token = "ada@example.com", "s3cret"
	f.want(f.do("GET", "/rest/api/3/myself", nil), 200)

	if n, _ := CountRequests(f.store.RequestLog, "GET", "/rest/api/3/myself", 401); n != 4 {
		t.Fatalf("logged %d 401s, want 4", n)
	}
}

func TestShutdownRoute(t *testing.T) {
	f := newFixture(t)
	f.want(f.doAuth("POST", ShutdownPath, nil, false), 404)
	var called atomic.Int32
	done := make(chan struct{})
	f.store.OnShutdown = func() {
		if called.Add(1) == 1 {
			close(done)
		}
	}
	f.want(f.doAuth("GET", ShutdownPath, nil, false), 405)
	f.want(f.doAuth("POST", ShutdownPath, nil, false), 202)
	select {
	case <-done:
	case <-time.After(wait.Timeout(10 * time.Second)):
		t.Fatal("OnShutdown never called")
	}
}

// TestTwoStoresOnOneCorpusLoseNothing is review F11: a test's own Store and
// a cmd/fakejira process are two Stores on one corpus file, each doing
// load-modify-save. The lock file beside the corpus serialises them, so
// every write lands and no comment id repeats.
func TestTwoStoresOnOneCorpusLoseNothing(t *testing.T) {
	corpus := filepath.Join(t.TempDir(), "corpus.json")
	a, b := &Store{Path: corpus}, &Store{Path: corpus}
	start, err := a.Load()
	if err != nil {
		t.Fatal(err)
	}
	const each = 40
	body := json.RawMessage(`{"type":"doc","version":1,"content":[]}`)
	var wg sync.WaitGroup
	errs := make(chan error, 2*each)
	for _, s := range []*Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				if _, err := s.AddComment("VIN-2", body, "", nil); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	end, err := b.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(end.Comments), len(start.Comments)+2*each; got != want {
		t.Fatalf("%d comments, want %d: concurrent saves lost writes", got, want)
	}
	seen := map[string]bool{}
	for _, c := range end.Comments {
		if seen[c.ID] {
			t.Fatalf("comment id %s repeated", c.ID)
		}
		seen[c.ID] = true
	}
}
