package github

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Timeouts. RemoteTimeout is modeled on gitx.RemoteTimeout and sized for the
// same reason: a call that crosses the network needs more than a local
// object-database read and less than a worktree checkout. ProbeTimeout bounds
// the credential probe, which is a local `gh auth status` at worst and must
// not park a picker behind an unreachable host.
const (
	RemoteTimeout = 60 * time.Second
	ProbeTimeout  = 10 * time.Second
)

// DefaultListLimit is how many issues a listing fetches when the caller names
// no bound. The picker filters what it is given locally, so this is the size
// of "the recent open issues", not a page size a user pages through.
const DefaultListLimit = 50

// probeTTL is how long a credential resolution is reused (task 035
// decision 4). Short enough that `gh auth login` in another terminal is
// noticed while the form is still open, long enough that the TUI opening the
// new-task form does not exec `gh` on every keystroke.
const probeTTL = 60 * time.Second

// Credential kinds, reported so `vincent doctor` and the API can say which
// leg answered without either one knowing how the legs work.
const (
	ViaGH    = "gh"
	ViaToken = "token"
)

// tokenVars are the environment variables consulted for the REST leg, in
// order. They are `gh`'s own names, because the daemon inherits the user's
// environment and that is where a token already is (spec §2).
var tokenVars = []string{"GITHUB_TOKEN", "GH_TOKEN"}

// Options configure a Client. Every zero value is the production default, so
// `New(Options{})` is the daemon's client and tests fill in only the seam
// they need.
type Options struct {
	// GHPath overrides the `gh` binary; empty resolves `gh` from PATH.
	GHPath string
	// Getenv reads the daemon's inherited environment; nil means os.Getenv.
	Getenv func(string) string
	// HTTP is the REST leg's client; nil means a client bounded by
	// RemoteTimeout.
	HTTP *http.Client
	// BaseURL is the REST API root; empty means https://api.github.com.
	// Tests point it at an httptest server, which is what keeps the REST leg
	// testable without the network.
	BaseURL string
	// Now stamps Issue.FetchedAt and ages the probe cache; nil means
	// time.Now.
	Now func() time.Time
	// Logger receives the concrete reason a call failed — the detail that is
	// deliberately kept out of every API response (decision 1). Nil
	// discards it.
	Logger *slog.Logger
}

// Client reads GitHub issues and pull requests for the daemon. It is safe
// for concurrent use.
type Client struct {
	opts Options

	mu      sync.Mutex
	cred    credential
	credErr error
	credAt  time.Time
}

// New returns a Client. It performs no I/O: the first Probe or call resolves
// the credential.
func New(opts Options) *Client { return &Client{opts: opts} }

// credential is how this daemon will talk to GitHub.
type credential struct {
	via string
	// ghPath is the resolved `gh` binary (ViaGH only).
	ghPath string
	// token is the inherited environment's token (ViaToken only).
	token string
}

// Availability is the answer `GET /v1/projects/{id}/github` renders: whether
// this project can be asked about issues at all, and if not, which named
// reason applies (task 035 decision 4).
type Availability struct {
	// Repo is `owner/name` when the project's origin identified one, empty
	// otherwise.
	Repo      string `json:"repo,omitempty"`
	Available bool   `json:"available"`
	// Reason is one of the reason constants, empty when Available.
	Reason string `json:"reason,omitempty"`
	// Message is Reason rendered for a human; empty when Available.
	Message string `json:"message,omitempty"`
	// Via is ViaGH or ViaToken when Available, so a diagnostic can say which
	// credential answered without probing again.
	Via string `json:"via,omitempty"`
}

// Probe reports whether this daemon can read repo's issues. It never returns
// an error: "no" with a reason *is* the answer this endpoint exists to give.
func (c *Client) Probe(ctx context.Context, repo Repo) Availability {
	if repo.Zero() {
		return Availability{Reason: ReasonNotGitHub, Message: Message(ReasonNotGitHub)}
	}
	cred, err := c.credential(ctx)
	if err != nil {
		reason := ReasonOf(err)
		c.logf("github credential unavailable", "repo", repo.String(), "reason", reason, "detail", err)
		return Availability{Repo: repo.String(), Reason: reason, Message: Message(reason)}
	}
	return Availability{Repo: repo.String(), Available: true, Via: cred.via}
}

// ListOptions bound a listing. The zero value is "the most recent open
// issues", which is what the picker opens with.
type ListOptions struct {
	// State is open (default), closed or all.
	State string
	// Limit caps the rows; <= 0 means DefaultListLimit.
	Limit int
}

func (o ListOptions) state() string {
	switch strings.ToLower(strings.TrimSpace(o.State)) {
	case "", StateOpen:
		return StateOpen
	case StateClosed:
		return StateClosed
	case "all":
		return "all"
	default:
		return StateOpen
	}
}

func (o ListOptions) limit() int {
	if o.Limit <= 0 {
		return DefaultListLimit
	}
	return o.Limit
}

// List returns repo's issues, newest first. Pull requests are never included:
// this is an issue picker, and PR work is task 035 decision 10's deliberate
// non-goal.
func (c *Client) List(ctx context.Context, repo Repo, opts ListOptions) ([]Issue, error) {
	cred, err := c.credential(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	var issues []Issue
	if cred.via == ViaGH {
		issues, err = c.ghList(ctx, cred, repo, opts)
	} else {
		issues, err = c.restList(ctx, cred, repo, opts)
	}
	if err != nil {
		c.logf("github issue list failed", "repo", repo.String(),
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return nil, err
	}
	sortIssues(issues)
	return issues, nil
}

// Get returns one issue. A pull request number answers ReasonNotFound: `gh
// issue view` refuses it, and the REST leg filters it, so both legs agree.
func (c *Client) Get(ctx context.Context, repo Repo, number int) (Issue, error) {
	cred, err := c.credential(ctx)
	if err != nil {
		return Issue{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	var issue Issue
	if cred.via == ViaGH {
		issue, err = c.ghGet(ctx, cred, repo, number)
	} else {
		issue, err = c.restGet(ctx, cred, repo, number)
	}
	if err != nil {
		c.logf("github issue fetch failed", "repo", repo.String(), "issue", number,
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return Issue{}, err
	}
	return issue, nil
}

// ListPulls returns repo's pull requests, newest first. The listing the API
// serves is open-only: an open listing is what a "which of my branches has a
// PR" screen is asking, and pulling a repository's whole pull-request history
// to answer a question about one row is what GetPull exists to avoid.
func (c *Client) ListPulls(ctx context.Context, repo Repo, opts ListOptions) ([]PullRequest, error) {
	cred, err := c.credential(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	var pulls []PullRequest
	if cred.via == ViaGH {
		pulls, err = c.ghListPulls(ctx, cred, repo, opts)
	} else {
		pulls, err = c.restListPulls(ctx, cred, repo, opts)
	}
	if err != nil {
		c.logf("github pull request list failed", "repo", repo.String(),
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return nil, err
	}
	sortPulls(pulls)
	return pulls, nil
}

// GetPull returns one pull request, in any state.
//
// It is the merged case's only answer. A task's link outlives the pull
// request's presence in an open listing — that is the whole point of storing
// it — so the task workspace reads its PR here rather than looking for it in
// a listing that by then no longer carries it.
func (c *Client) GetPull(ctx context.Context, repo Repo, number int) (PullRequest, error) {
	cred, err := c.credential(ctx)
	if err != nil {
		return PullRequest{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteTimeout)
	defer cancel()
	var pull PullRequest
	if cred.via == ViaGH {
		pull, err = c.ghGetPull(ctx, cred, repo, number)
	} else {
		pull, err = c.restGetPull(ctx, cred, repo, number)
	}
	if err != nil {
		c.logf("github pull request fetch failed", "repo", repo.String(), "pull", number,
			"via", cred.via, "reason", ReasonOf(err), "detail", err)
		return PullRequest{}, err
	}
	return pull, nil
}

// credential resolves how to talk to GitHub, memoized for probeTTL.
//
// `gh` first, because it carries the user's own host, enterprise and SSO
// configuration; the environment token second, because §2 says the daemon
// inherits the user's environment rather than storing a secret. A `gh` that
// is installed but logged out falls through to the token rather than failing:
// the two legs are alternatives, not a chain that stops at the first
// disappointment (decision 1).
func (c *Client) credential(ctx context.Context) (credential, error) {
	now := c.now()
	c.mu.Lock()
	if !c.credAt.IsZero() && now.Sub(c.credAt) < probeTTL {
		cred, err := c.cred, c.credErr
		c.mu.Unlock()
		return cred, err
	}
	c.mu.Unlock()

	cred, err := c.resolveCredential(ctx)

	c.mu.Lock()
	c.cred, c.credErr, c.credAt = cred, err, now
	c.mu.Unlock()
	return cred, err
}

func (c *Client) resolveCredential(ctx context.Context) (credential, error) {
	if path, ok := c.lookGH(); ok && c.ghAuthenticated(ctx, path) {
		return credential{via: ViaGH, ghPath: path}, nil
	}
	for _, name := range tokenVars {
		if token := strings.TrimSpace(c.getenv(name)); token != "" {
			return credential{via: ViaToken, token: token}, nil
		}
	}
	return credential{}, &Error{Reason: ReasonNoCredential}
}

// lookGH resolves the `gh` binary. A configured path is used as given so an
// operator can point at a binary that is not on the daemon's PATH.
func (c *Client) lookGH() (string, bool) {
	if c.opts.GHPath != "" {
		return c.opts.GHPath, true
	}
	path, err := exec.LookPath("gh")
	if err != nil {
		return "", false
	}
	return path, true
}

func (c *Client) getenv(key string) string {
	if c.opts.Getenv != nil {
		return c.opts.Getenv(key)
	}
	return os.Getenv(key)
}

func (c *Client) now() time.Time {
	if c.opts.Now != nil {
		return c.opts.Now()
	}
	return time.Now()
}

func (c *Client) logf(msg string, args ...any) {
	if c.opts.Logger != nil {
		c.opts.Logger.Debug(msg, args...)
	}
}

// Detection is what `vincent doctor` reports about this daemon's ability to
// read GitHub (task 035): the `gh` CLI's presence, path, version and login
// state, and whether a token was inherited instead. It is the environment
// picture, deliberately separate from Probe, which is about one repository.
type Detection struct {
	// GHFound and GHPath describe the `gh` binary; GHVersion is its raw
	// `gh --version` first line, empty when it could not be asked.
	GHFound   bool   `json:"gh_found"`
	GHPath    string `json:"gh_path,omitempty"`
	GHVersion string `json:"gh_version,omitempty"`
	// GHAuthenticated is `gh auth status` exiting zero. An installed but
	// logged-out `gh` is the case the row exists to name: it probes as
	// present and then answers nothing.
	GHAuthenticated bool `json:"gh_authenticated"`
	// TokenVar names the environment variable a token was found in
	// (GITHUB_TOKEN or GH_TOKEN), empty when neither is set. The value is
	// never reported — this is a diagnostic, not a secret dump.
	TokenVar string `json:"token_var,omitempty"`
	// Via is how a call would be made (ViaGH or ViaToken), empty when no
	// credential is available; Reason then says so.
	Via    string `json:"via,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Usable reports that some credential would answer.
func (d Detection) Usable() bool { return d.Via != "" }

// Detect resolves the environment picture, bypassing the probe cache: a
// diagnostic that reported a minute-old answer would be the wrong tool for
// "I just ran `gh auth login`, why does vincent still say no".
func (c *Client) Detect(ctx context.Context) Detection {
	var d Detection
	if path, ok := c.lookGH(); ok {
		d.GHFound, d.GHPath = true, path
		d.GHVersion = c.ghVersion(ctx, path)
		d.GHAuthenticated = c.ghAuthenticated(ctx, path)
		if d.GHAuthenticated {
			d.Via = ViaGH
		}
	}
	for _, name := range tokenVars {
		if strings.TrimSpace(c.getenv(name)) != "" {
			d.TokenVar = name
			break
		}
	}
	if d.Via == "" && d.TokenVar != "" {
		d.Via = ViaToken
	}
	if d.Via == "" {
		d.Reason = ReasonNoCredential
	}
	return d
}
