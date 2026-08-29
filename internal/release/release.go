package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// Repo is the project's own repository. It is a constant and not a config
// key on purpose: this is "is there a newer *vincent*", not a generic
// release watcher, and a settable repository would be an unaudited URL the
// daemon fetches on a timer.
const Repo = "lezli01/vincent"

// DefaultBaseURL is GitHub's REST root. Tests point Options.BaseURL at an
// httptest server, which is what keeps the whole transport testable without
// the network — the same seam internal/github's REST leg uses.
const DefaultBaseURL = "https://api.github.com"

// Timeout bounds the one call. It is short because nothing waits on the
// answer: the poller has a whole interval to try again and `vincent update
// --check` is a human at a prompt who would rather hear "the check failed"
// than watch a socket.
const Timeout = 10 * time.Second

// Release is the normalized answer: what the latest stable release is, when
// it was published and where a human can read about it.
type Release struct {
	// Version is the tag with its leading "v" — "v0.4.1". Comparison
	// normalizes, but everything that renders one wants the canonical
	// spelling, and the download URLs are built from it.
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"published_at"`
	URL         string    `json:"url"`
}

// Options configure a Client. Every zero value is the production default,
// so `New(Options{})` is the daemon's client.
type Options struct {
	// BaseURL is the REST API root; empty means DefaultBaseURL.
	BaseURL string
	// HTTP is the transport; nil means a client bounded by Timeout.
	HTTP *http.Client
}

// Client reads the latest stable release. It is safe for concurrent use —
// it holds no mutable state.
type Client struct {
	base string
	http *http.Client
}

// New builds a client. It performs no I/O.
func New(opts Options) *Client {
	base := strings.TrimSuffix(opts.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	h := opts.HTTP
	if h == nil {
		h = &http.Client{Timeout: Timeout}
	}
	return &Client{base: base, http: h}
}

// latestBody is the subset of GitHub's release object this reads. Everything
// else on that payload — author, assets, body, reactions — is deliberately
// ignored rather than parsed and dropped, so a field appearing or changing
// upstream cannot break the check.
type latestBody struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
}

// maxBody caps what is read off the wire. A release body can carry an entire
// changelog, and this parses four fields out of it; there is no reason to
// hold megabytes in the daemon's heap on a timer.
const maxBody = 1 << 20

// Latest fetches the latest stable release.
//
// A draft, a prerelease or a tag carrying a semver prerelease suffix is not
// an answer: it returns an error rather than a Release, because "the latest
// release is a release candidate" and "there is no release" are the same
// thing to a user on a stable channel.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.base, Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, fmt.Errorf("build release request: %w", err)
	}
	// Accept is the only header set. No Authorization — this endpoint is
	// public and a token would make the call identifying — and no User-Agent
	// beyond Go's default, which says nothing about this install (§16).
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("fetch latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// 403 and 429 are the rate limit, which is the expected failure for
		// an unauthenticated call and is why the poll interval is a day.
		// They are not special-cased: every non-200 degrades identically.
		return Release{}, fmt.Errorf("fetch latest release: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Release{}, fmt.Errorf("read latest release: %w", err)
	}
	var body latestBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return Release{}, fmt.Errorf("parse latest release: %w", err)
	}
	return fromBody(body)
}

// fromBody normalizes and screens one release object. It is separate from
// Latest so the table-driven parse tests run against captured bodies without
// a transport.
func fromBody(body latestBody) (Release, error) {
	tag := strings.TrimSpace(body.TagName)
	if tag == "" {
		return Release{}, fmt.Errorf("latest release has no tag")
	}
	v := Canonical(tag)
	if v == "" {
		return Release{}, fmt.Errorf("latest release tag %q is not a semantic version", tag)
	}
	// The client-side half of "stable only". `releases/latest` already
	// excludes both, so reaching here means the API disagreed with its own
	// documentation or somebody pointed BaseURL somewhere else; either way
	// the promise holds.
	if body.Draft || body.Prerelease || semver.Prerelease(v) != "" {
		return Release{}, fmt.Errorf("latest release %s is not a stable release", tag)
	}
	rel := Release{Version: v, URL: strings.TrimSpace(body.HTMLURL)}
	if t, err := time.Parse(time.RFC3339, body.PublishedAt); err == nil {
		rel.PublishedAt = t.UTC()
	}
	if rel.URL == "" {
		rel.URL = fmt.Sprintf("https://github.com/%s/releases/tag/%s", Repo, tag)
	}
	return rel, nil
}

// Canonical normalizes a version for comparison: it adds the "v" prefix
// goreleaser strips when it injects {{.Version}} into internal/version, and
// returns "" for anything semver cannot read.
//
// This is the whole reason comparison is a function here rather than a
// `<` at each call site: the same release is spelled "0.4.1" in the binary
// and "v0.4.1" in the tag, and a naive comparison of the two says the
// binary is older than every release forever.
func Canonical(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return semver.Canonical(v)
}

// IsNewer reports whether latest is a strictly newer stable version than
// current.
//
// A `dev` build — plain `go build`, version.Version()'s fallback — is never
// behind. It has no place on the version line: it may be newer than every
// release or older than all of them, and telling a developer building from
// source to download a release is noise at best and wrong at worst.
func IsNewer(current, latest string) bool {
	c, l := Canonical(current), Canonical(latest)
	if c == "" || l == "" {
		return false
	}
	// A prerelease never surfaces as an update, wherever it came from.
	if semver.Prerelease(l) != "" {
		return false
	}
	return semver.Compare(l, c) > 0
}

// Status is a cached check result: what the last poll learned, when, and why
// it failed if it did.
//
// It lives in this leaf package rather than in the daemon that fills it
// because internal/api serves it and internal/daemon produces it, and neither
// may import the other. Its zero value is the never-polled state, which is a
// distinct answer from "no update available" — a daemon ten seconds old
// genuinely does not know yet, and a client that renders the two the same way
// is claiming a check happened that did not.
type Status struct {
	// Enabled is whether the poller is configured to run, as of the last
	// tick. A client showing an empty row wants to be able to say "because
	// the check is off" rather than "because something is broken".
	Enabled bool `json:"enabled"`
	// CheckedAt is when a poll last *succeeded*; zero if none ever has.
	CheckedAt time.Time `json:"checked_at"`
	// Latest is the newest stable release seen. Zero until a poll succeeds.
	Latest Release `json:"latest"`
	// Error is why the last attempt failed, empty when it worked. It is
	// carried rather than swallowed: `vincent doctor` is where a user asks
	// why a row is empty, and "403 API rate limit exceeded" answers it.
	Error string `json:"error,omitempty"`
}

// UpdateAvailable reports whether the cached release is newer than current.
// It is a method so the `dev`-build and missing-`v` rules are applied in one
// place rather than at every renderer.
func (s Status) UpdateAvailable(current string) bool {
	return IsNewer(current, s.Latest.Version)
}
