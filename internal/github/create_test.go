package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The one write path (task 069). Both legs are held to the same bar the read
// side is: one normalized PullRequest out of a create, against captured
// real-CLI and real-API output named for the version it came from, and named
// reasons for the two failures a create has that a read does not.

// The `gh` leg. `gh pr create` prints the pull request's URL and nothing
// else — there is no `--json` on it — so the number is parsed out of that URL
// and the pull request is read back through `pr view`, which is the shape
// this package already normalizes.
func TestCreatePullGH(t *testing.T) {
	c, argv := ghClient(t, "success")
	created := filepath.Join(t.TempDir(), "created.json")
	t.Setenv("FAKEGH_CREATED_FILE", created)

	pull, err := c.CreatePull(context.Background(), Repo{Owner: "octo", Name: "repo"}, CreateOptions{
		Base: "master", Head: "vincent/7-open-a-pr",
		Title: "Open a pull request from vincent itself",
		Body:  "Pushes the branch and creates the pull request.\n\nCloses #273",
		Draft: true,
	})
	if err != nil {
		t.Fatalf("CreatePull: %v", err)
	}
	if pull.Number == 0 || pull.Repo != "octo/repo" {
		t.Fatalf("created pull request is %+v", pull)
	}
	if pull.Title != "Open a pull request from vincent itself" {
		t.Errorf("title is %q", pull.Title)
	}
	if !pull.Draft {
		t.Error("--draft did not produce a draft pull request")
	}
	if pull.HeadBranch != "vincent/7-open-a-pr" || pull.BaseBranch != "master" {
		t.Errorf("head/base are %q/%q", pull.HeadBranch, pull.BaseBranch)
	}
	if pull.State != StateOpen || pull.Merged {
		t.Errorf("a new pull request is %s (merged=%v)", pull.State, pull.Merged)
	}
	// The argv is the contract with the real CLI, and the body is not in it:
	// `--body-file -` is what keeps a long description off Windows' 32 KiB
	// command line.
	recorded := recordedArgv(t, argv)
	for _, want := range []string{
		"pr create", "--repo octo/repo", "--base master",
		"--head vincent/7-open-a-pr", "--body-file -", "--draft",
	} {
		if !strings.Contains(recorded, want) {
			t.Errorf("gh argv %q does not carry %q", recorded, want)
		}
	}
}

// The whole argv, so anything added to it is added here too — and so `--draft`
// is present exactly when it was asked for. `gh` has no `--ready`
// counterpart: a pull request created without `--draft` is ready.
func TestGHCreateArgs(t *testing.T) {
	repo := Repo{Owner: "octo", Name: "repo"}
	want := []string{
		"pr", "create", "--repo", "octo/repo", "--base", "master",
		"--head", "topic", "--title", "T", "--body-file", "-",
	}
	got := ghCreateArgs(repo, CreateOptions{Base: "master", Head: "topic", Title: "T"})
	if !slices.Equal(got, want) {
		t.Fatalf("ready argv is %v, want %v", got, want)
	}
	got = ghCreateArgs(repo, CreateOptions{Base: "master", Head: "topic", Title: "T", Draft: true})
	if !slices.Equal(got, append(want, "--draft")) {
		t.Fatalf("draft argv is %v", got)
	}
}

// The number is read out of the URL the real CLI prints, warning lines and
// all. It is captured output, not a guess at the format.
func TestPullNumberFromURL(t *testing.T) {
	if n, ok := pullNumberFromURL(string(fixture(t, "gh_2.98.0_pr_create.txt"))); !ok || n != 812 {
		t.Fatalf("captured gh output parsed to %d (ok=%v), want 812", n, ok)
	}
	for _, bad := range []string{"", "not a url", "https://github.com/octo/repo", "https://github.com/o/r/pull/x"} {
		if n, ok := pullNumberFromURL(bad); ok {
			t.Errorf("pullNumberFromURL(%q) accepted %d", bad, n)
		}
	}
}

// The REST leg, against captured API output. A created pull request and a
// fetched one are the same resource, so this is the same restPull the read
// side already normalizes — one shape, not two.
func TestCreatePullREST(t *testing.T) {
	var body map[string]any
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(fixture(t, "rest_pull_created.json"))
	}))
	t.Cleanup(srv.Close)

	c := New(Options{
		BaseURL: srv.URL,
		Getenv:  func(string) string { return "tok" },
		GHPath:  filepath.Join(t.TempDir(), "no-gh-here"),
		Now:     func() time.Time { return fixtureNow },
	})
	pull, err := c.CreatePull(context.Background(), Repo{Owner: "octo", Name: "repo"}, CreateOptions{
		Base: "master", Head: "vincent/7-open-a-pr",
		Title: "Open a pull request from vincent itself",
		Body:  "Pushes the branch and creates the pull request.\n\nCloses #273",
		Draft: true,
	})
	if err != nil {
		t.Fatalf("CreatePull: %v", err)
	}
	if method != http.MethodPost || path != "/repos/octo/repo/pulls" {
		t.Fatalf("the REST leg made %s %s", method, path)
	}
	if body["head"] != "vincent/7-open-a-pr" || body["base"] != "master" || body["draft"] != true {
		t.Fatalf("request body is %v", body)
	}
	// Same normalized shape as the gh leg, which is what makes "a client can
	// never tell which one ran" true.
	if pull.Number != 812 || pull.Repo != "octo/repo" || !pull.Draft ||
		pull.HeadBranch != "vincent/7-open-a-pr" || pull.BaseBranch != "master" ||
		pull.State != StateOpen || pull.Author != "octocat" {
		t.Fatalf("normalized pull request is %+v", pull)
	}
}

// GitHub's own refusal when a pull request already exists for this head. It
// is a named reason on both legs, because "there is already one" is an answer
// a human can act on and "GitHub could not be reached" is not — and it is the
// backstop behind the double-submission refusal (decision 7).
func TestCreatePullDuplicateHeadIsNamedOnBothLegs(t *testing.T) {
	t.Run("rest", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(fixture(t, "rest_pull_create_duplicate_422.json"))
		}))
		t.Cleanup(srv.Close)
		c := New(Options{
			BaseURL: srv.URL, Getenv: func(string) string { return "tok" },
			GHPath: filepath.Join(t.TempDir(), "no-gh-here"),
		})
		_, err := c.CreatePull(context.Background(), Repo{Owner: "octo", Name: "repo"},
			CreateOptions{Base: "master", Head: "topic", Title: "T"})
		assertReasonAndNoLeak(t, err, ReasonPullExists, "Validation Failed")
	})
	t.Run("gh", func(t *testing.T) {
		c, _ := ghClient(t, "pr-exists")
		_, err := c.CreatePull(context.Background(), Repo{Owner: "octo", Name: "repo"},
			CreateOptions{Base: "master", Head: "topic", Title: "T"})
		assertReasonAndNoLeak(t, err, ReasonPullExists, "into branch")
	})
}

// A 403 is `forbidden` on both legs. The write path is where a read-scoped
// credential lands, and the API's job is to say so in one word rather than
// hand a client GitHub's sentence.
func TestCreatePullForbiddenIsNamedOnBothLegs(t *testing.T) {
	t.Run("rest", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
		}))
		t.Cleanup(srv.Close)
		c := New(Options{
			BaseURL: srv.URL, Getenv: func(string) string { return "tok" },
			GHPath: filepath.Join(t.TempDir(), "no-gh-here"),
		})
		_, err := c.CreatePull(context.Background(), Repo{Owner: "octo", Name: "repo"},
			CreateOptions{Base: "master", Head: "topic", Title: "T"})
		assertReasonAndNoLeak(t, err, ReasonForbidden, "not accessible")
	})
	t.Run("gh", func(t *testing.T) {
		c, _ := ghClient(t, "forbidden")
		_, err := c.CreatePull(context.Background(), Repo{Owner: "octo", Name: "repo"},
			CreateOptions{Base: "master", Head: "topic", Title: "T"})
		assertReasonAndNoLeak(t, err, ReasonForbidden, "not accessible")
	})
}

// A create with nothing to create is refused before a credential is even
// resolved, so an empty title costs no network call.
func TestCreatePullRefusesEmptyValues(t *testing.T) {
	c := New(Options{
		Getenv: func(string) string { return "" },
		GHPath: filepath.Join(t.TempDir(), "no-gh-here"),
	})
	for _, opts := range []CreateOptions{
		{Base: "master", Head: "topic"},
		{Base: "master", Title: "T"},
		{Head: "topic", Title: "T"},
	} {
		if _, err := c.CreatePull(context.Background(), Repo{Owner: "o", Name: "r"}, opts); ReasonOf(err) != ReasonBadRequest {
			t.Errorf("CreatePull(%+v) reported %q, want %q", opts, ReasonOf(err), ReasonBadRequest)
		}
	}
}

// assertReasonAndNoLeak is decision 1 held on the write path: the named
// reason is what a client is given, and neither leg's own text is in the
// message rendered for one.
func assertReasonAndNoLeak(t *testing.T, err error, want, leaked string) {
	t.Helper()
	if err == nil {
		t.Fatal("the create succeeded; it must fail")
	}
	if got := ReasonOf(err); got != want {
		t.Fatalf("reason is %q, want %q (err: %v)", got, want, err)
	}
	if msg := Message(ReasonOf(err)); strings.Contains(msg, leaked) {
		t.Fatalf("the rendered message %q leaks the leg's own text", msg)
	}
}
