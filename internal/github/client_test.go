package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/github/githubtest"
)

// No test in this file reaches the network. The `gh` leg runs against
// cmd/fakegh — a scenario-driven stand-in on three platforms, the cmd/fakeagent
// precedent — and the REST leg runs against httptest. That is what makes the
// cross-platform claim ("the gh invocation and the token fallback both work on
// Windows, macOS and Linux") something CI proves rather than something the
// design hopes for.

// ghClient returns a client whose `gh` is the fake, with no token in the
// environment so the gh leg is the one that answers.
func ghClient(t *testing.T, scenario string) (*Client, string) {
	t.Helper()
	path := githubtest.BuildFakeGH(t)
	t.Setenv("FAKEGH_SCENARIO", scenario)
	argv := filepath.Join(t.TempDir(), "argv.txt")
	t.Setenv("FAKEGH_ARGV_FILE", argv)
	return New(Options{
		GHPath: path,
		Getenv: func(string) string { return "" },
		Now:    func() time.Time { return fixtureNow },
	}), argv
}

func recordedArgv(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	return string(b)
}

func TestGHLegListsAndFetches(t *testing.T) {
	c, argv := ghClient(t, "success")
	repo := Repo{Owner: "octo", Name: "repo"}

	if a := c.Probe(t.Context(), repo); !a.Available || a.Via != ViaGH {
		t.Fatalf("probe = %+v, want available via gh", a)
	}
	issues, err := c.List(t.Context(), repo, ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("listed %d issues, want 2", len(issues))
	}
	if issues[0].Number != 200 {
		t.Errorf("first issue = #%d, want the newest (#200)", issues[0].Number)
	}
	if issues[0].Assignee != "hubot" || issues[0].MilestoneNumber != 4 {
		t.Errorf("metadata did not normalize: %+v", issues[0])
	}

	issue, err := c.Get(t.Context(), repo, 41)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.Number != 41 || issue.Repo != "octo/repo" {
		t.Errorf("Get returned %+v, want #41 of octo/repo", issue)
	}

	// The argv the daemon actually built, asserted rather than assumed: a
	// wrong `--json` field set is a parse that silently drops metadata.
	log := recordedArgv(t, argv)
	for _, want := range []string{
		"auth status",
		"issue list --repo octo/repo --state open --limit 50 --json " + ghFields,
		"issue view 41 --repo octo/repo --json " + ghFields,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("gh was not invoked with %q; log:\n%s", want, log)
		}
	}
}

// TestGHFailuresMapToReasons: every failure mode gets a named reason, and no
// `gh` stderr escapes into the reason itself (decision 1). The Detail field is
// where that text lives, and only the daemon log reads it.
func TestGHFailuresMapToReasons(t *testing.T) {
	for _, tc := range []struct{ scenario, want string }{
		{"not-found", ReasonNotFound},
		{"unauthorized", ReasonUnauthorized},
		{"rate-limited", ReasonRateLimited},
		{"forbidden", ReasonForbidden},
		{"bad-json", ReasonBadResponse},
	} {
		c, _ := ghClient(t, tc.scenario)
		_, err := c.List(t.Context(), Repo{Owner: "octo", Name: "repo"}, ListOptions{})
		if err == nil {
			t.Errorf("%s: List succeeded, want a failure", tc.scenario)
			continue
		}
		if got := ReasonOf(err); got != tc.want {
			t.Errorf("%s: reason = %q, want %q (detail was %q)", tc.scenario, got, tc.want, err)
		}
		if strings.Contains(Message(ReasonOf(err)), "gh:") {
			t.Errorf("%s: the client-facing message leaked gh's own text", tc.scenario)
		}
	}
}

// TestGHLoggedOutFallsThroughToTheToken: `gh` present but logged out is not a
// dead end. The two legs are alternatives, not a chain that stops at the first
// disappointment.
func TestGHLoggedOutFallsThroughToTheToken(t *testing.T) {
	path := githubtest.BuildFakeGH(t)
	t.Setenv("FAKEGH_SCENARIO", "logged-out")
	c := New(Options{
		GHPath: path,
		Getenv: func(key string) string {
			if key == "GH_TOKEN" {
				return "t0ken"
			}
			return ""
		},
	})
	a := c.Probe(t.Context(), Repo{Owner: "octo", Name: "repo"})
	if !a.Available || a.Via != ViaToken {
		t.Fatalf("probe = %+v, want available via the environment token", a)
	}
}

// TestNoCredentialAtAll is the row `vincent doctor` exists to explain: no
// `gh`, no token, one named reason, and nothing else in vincent affected.
func TestNoCredentialAtAll(t *testing.T) {
	c := New(Options{
		GHPath: filepath.Join(t.TempDir(), "definitely-not-gh"),
		Getenv: func(string) string { return "" },
	})
	a := c.Probe(t.Context(), Repo{Owner: "octo", Name: "repo"})
	if a.Available {
		t.Fatal("probe reported available with no gh and no token")
	}
	if a.Reason != ReasonNoCredential {
		t.Errorf("reason = %q, want %q", a.Reason, ReasonNoCredential)
	}
	if _, err := c.List(t.Context(), Repo{Owner: "octo", Name: "repo"}, ListOptions{}); ReasonOf(err) != ReasonNoCredential {
		t.Errorf("List reason = %q, want %q", ReasonOf(err), ReasonNoCredential)
	}
}

// TestProbeIsCached: the form probes on every project selection, and the
// picker must not exec `gh auth status` per keystroke (decision 4).
func TestProbeIsCached(t *testing.T) {
	c, argv := ghClient(t, "success")
	repo := Repo{Owner: "octo", Name: "repo"}
	for range 5 {
		if a := c.Probe(t.Context(), repo); !a.Available {
			t.Fatalf("probe = %+v, want available", a)
		}
	}
	if got := strings.Count(recordedArgv(t, argv), "auth status"); got != 1 {
		t.Errorf("gh auth status ran %d times for five probes, want 1 (the TTL cache)", got)
	}
}

// TestGHTimeoutIsItsOwnReason: a `gh` that never answers must not park the
// caller and must not be reported as "unreachable", which reads as a network
// verdict this call never reached.
func TestGHTimeoutIsItsOwnReason(t *testing.T) {
	path := githubtest.BuildFakeGH(t)
	t.Setenv("FAKEGH_SCENARIO", "hang")
	c := New(Options{GHPath: path, Getenv: func(string) string { return "" }})
	// The probe would hang too, so hand the leg a resolved credential and
	// call it with a context that is already on a short fuse.
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err := c.ghList(ctx, credential{via: ViaGH, ghPath: path},
		Repo{Owner: "octo", Name: "repo"}, ListOptions{})
	if got := ReasonOf(err); got != ReasonTimeout {
		t.Errorf("reason = %q, want %q", got, ReasonTimeout)
	}
}

// restClient points the REST leg at an httptest server and denies it any
// `gh`, so the token path is the one under test.
func restClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Options{
		GHPath:  filepath.Join(t.TempDir(), "no-gh-here"),
		BaseURL: srv.URL,
		HTTP:    srv.Client(),
		Getenv: func(key string) string {
			if key == "GITHUB_TOKEN" {
				return "t0ken"
			}
			return ""
		},
		Now: func() time.Time { return fixtureNow },
	})
}

func TestRESTLegListsAndFetches(t *testing.T) {
	var seen []string
	var auth string
	c := restClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		auth = r.Header.Get("Authorization")
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues"):
			_, _ = w.Write(fixture(t, "rest_issues.json"))
		default:
			_, _ = w.Write(fixture(t, "rest_issue_same.json"))
		}
	})
	repo := Repo{Owner: "lezli01", Name: "vincent"}

	if a := c.Probe(t.Context(), repo); !a.Available || a.Via != ViaToken {
		t.Fatalf("probe = %+v, want available via token", a)
	}
	issues, err := c.List(t.Context(), repo, ListOptions{Limit: 6})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("List returned nothing")
	}
	if auth != "Bearer t0ken" {
		t.Errorf("Authorization = %q, want the inherited token", auth)
	}
	if !strings.Contains(seen[0], "per_page=6") || !strings.Contains(seen[0], "state=open") {
		t.Errorf("list request = %q, want state and per_page", seen[0])
	}
	if _, err := c.Get(t.Context(), repo, 81133); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

// TestRESTFailuresMapToReasons: HTTP statuses become the same named reasons
// the `gh` leg produces, and the response body never becomes the message.
func TestRESTFailuresMapToReasons(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		want    string
	}{
		{"unauthorized", http.StatusUnauthorized, nil, ReasonUnauthorized},
		{"forbidden", http.StatusForbidden, nil, ReasonForbidden},
		{
			"rate limited by header", http.StatusForbidden,
			map[string]string{"X-RateLimit-Remaining": "0"},
			ReasonRateLimited,
		},
		{"rate limited by status", http.StatusTooManyRequests, nil, ReasonRateLimited},
		{"not found", http.StatusNotFound, nil, ReasonNotFound},
		{"server error", http.StatusBadGateway, nil, ReasonUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := restClient(t, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"secret internal detail"}`))
			})
			_, err := c.List(t.Context(), Repo{Owner: "octo", Name: "repo"}, ListOptions{})
			if got := ReasonOf(err); got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
			if strings.Contains(Message(ReasonOf(err)), "secret internal detail") {
				t.Error("the client-facing message leaked the response body")
			}
		})
	}
}

// TestRESTGetRefusesAPullRequest: `gh issue view` refuses a PR number, so the
// REST leg must too — otherwise which leg answered would decide whether a PR
// could be linked to a task.
func TestRESTGetRefusesAPullRequest(t *testing.T) {
	c := restClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"number":201,"title":"a pr","html_url":"u","state":"open",
			"pull_request":{"url":"https://api.github.com/repos/o/r/pulls/201"}}`))
	})
	_, err := c.Get(t.Context(), Repo{Owner: "octo", Name: "repo"}, 201)
	if got := ReasonOf(err); got != ReasonNotFound {
		t.Errorf("reason = %q, want %q", got, ReasonNotFound)
	}
}

// TestDetectReportsTheEnvironment is the `vincent doctor` row's source.
func TestDetectReportsTheEnvironment(t *testing.T) {
	path := githubtest.BuildFakeGH(t)
	t.Setenv("FAKEGH_SCENARIO", "success")
	c := New(Options{GHPath: path, Getenv: func(string) string { return "" }})
	d := c.Detect(t.Context())
	if !d.GHFound || !d.GHAuthenticated || d.Via != ViaGH {
		t.Errorf("detection = %+v, want a found, authenticated gh", d)
	}
	if d.TokenVar != "" {
		t.Errorf("token_var = %q with no token in the environment", d.TokenVar)
	}

	t.Setenv("FAKEGH_SCENARIO", "logged-out")
	c = New(Options{GHPath: path, Getenv: func(key string) string {
		if key == "GITHUB_TOKEN" {
			return "t0ken"
		}
		return ""
	}})
	d = c.Detect(t.Context())
	switch {
	case !d.GHFound || d.GHAuthenticated:
		t.Errorf("detection = %+v, want gh found but logged out", d)
	case d.TokenVar != "GITHUB_TOKEN":
		t.Errorf("token_var = %q, want the variable's *name*", d.TokenVar)
	case d.Via != ViaToken:
		t.Errorf("via = %q, want the token fallback", d.Via)
	}

	c = New(Options{
		GHPath: filepath.Join(t.TempDir(), "nope"),
		Getenv: func(string) string { return "" },
	})
	if d := c.Detect(t.Context()); d.Usable() || d.Reason != ReasonNoCredential {
		t.Errorf("detection = %+v, want unusable with %q", d, ReasonNoCredential)
	}
}
