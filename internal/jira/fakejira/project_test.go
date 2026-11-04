package fakejira

import (
	"strings"
	"testing"
	"time"
)

func TestProjectRoute(t *testing.T) {
	f := newFixture(t)
	for _, idOrKey := range []string{"VIN", "10000"} {
		a := f.want(f.do("GET", "/rest/api/3/project/"+idOrKey, nil), 200)
		if a.body["id"] != "10000" || a.body["key"] != "VIN" || a.body["name"] != "Vincent" {
			t.Errorf("project %s: %s", idOrKey, a.raw)
		}
	}
	a := f.want(f.do("GET", "/rest/api/3/project/NOPE", nil), 404)
	if msgs, _ := a.body["errorMessages"].([]any); len(msgs) != 1 || !strings.Contains(msgs[0].(string), "NOPE") {
		t.Errorf("unknown project: %s", a.raw)
	}
}

// TestRetiredSearchIsGoneAndLogged: /rest/api/3/search answers 410, as
// Atlassian's does since CHANGE-2046, and the request log tells it apart
// from search/jql so a test can assert a client never strayed onto it.
func TestRetiredSearchIsGoneAndLogged(t *testing.T) {
	f := newFixture(t)
	f.want(f.do("GET", "/rest/api/3/search?jql=project%20%3D%2010000", nil), 410)
	f.want(f.do("GET", search(`project = 10000`), nil), 200)
	for path, want := range map[string]int{"rest/api/3/search": 1, "rest/api/3/search/jql": 1} {
		n, err := CountRequests(f.store.RequestLog, "GET", path, 0)
		if err != nil || n != want {
			t.Errorf("%s: %d requests (%v), want %d", path, n, err, want)
		}
	}
}

func TestSeedIssuesPagesPast500(t *testing.T) {
	f := newFixture(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var ids []string
	if err := f.store.Update(func(c *Corpus) error {
		ids = c.SeedIssues("VIN", 600, base)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 600 || ids[0] != "10004" || ids[599] != "10603" {
		t.Fatalf("seeded ids %s … %s (%d)", ids[0], ids[len(ids)-1], len(ids))
	}
	seen, tok := 0, ""
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("never reached the last page")
		}
		extra := []string{"maxResults", "100"}
		if tok != "" {
			extra = append(extra, "nextPageToken", tok)
		}
		a := f.want(f.do("GET", search(`project = 10000 AND statusCategory != Done ORDER BY id ASC`, extra...), nil), 200)
		seen += len(keys(t, a))
		next, ok := a.body["nextPageToken"].(string)
		if !ok {
			break
		}
		tok = next
	}
	// VIN-1 and VIN-2 are open in the built-in corpus; VIN-3 is done.
	if seen != 602 {
		t.Fatalf("searched %d open issues, want 602", seen)
	}
}

func TestIssueCommentField(t *testing.T) {
	f := newFixture(t)
	a := f.want(f.do("GET", "/rest/api/3/issue/VIN-1?fields=comment", nil), 200)
	if total := path(a.body, "fields", "comment", "total"); total != float64(2) {
		t.Fatalf("comment.total = %v: %s", total, a.raw)
	}
}
