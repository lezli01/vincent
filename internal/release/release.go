package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
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
	// Assets are read by Master only: a master build's version is the one
	// its archives are named with.
	Assets []struct {
		Name string `json:"name"`
	} `json:"assets"`
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
	body, err := c.get(ctx, "latest", "latest release")
	if err != nil {
		return Release{}, err
	}
	return fromBody(body)
}

// get reads one release of Repo — "latest", or "tags/<tag>" — as what names
// it in an error.
func (c *Client) get(ctx context.Context, which, what string) (latestBody, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/%s", c.base, Repo, which)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return latestBody{}, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return latestBody{}, fmt.Errorf("fetch %s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return latestBody{}, fmt.Errorf("fetch %s: %s", what, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return latestBody{}, fmt.Errorf("read %s: %w", what, err)
	}
	var body latestBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return latestBody{}, fmt.Errorf("parse %s: %w", what, err)
	}
	return body, nil
}

// MasterTag is the rolling prerelease the master-build workflow replaces on
// every push to master (task 147): the newest build of master, signed and
// checksummed exactly as a release is. It is never a semantic version, so
// it can never be mistaken for a release, and it is not named `master`,
// which would make the tag and the branch an ambiguous ref.
const MasterTag = "master-latest"

// masterArchive matches a master build's archive name, as
// `.goreleaser.yaml`'s `name_template` writes it, capturing the version.
var masterArchive = regexp.MustCompile(`^vincent_(.+)_(?:linux|darwin|windows)_(?:amd64|arm64)\.(?:tar\.gz|zip)$`)

// Master reads the newest master build (task 147). Its Version is the
// build's own, read off its archive names — `0.21.1-master.5.gabc1234`, the
// patch after the newest release, the commits since it and the commit — so
// it is a semantic version that sorts below the release it precedes and is
// never offered by Latest or IsNewer.
func (c *Client) Master(ctx context.Context) (Release, error) {
	body, err := c.get(ctx, "tags/"+MasterTag, "the latest master build")
	if err != nil {
		return Release{}, err
	}
	if strings.TrimSpace(body.TagName) != MasterTag || body.Draft {
		return Release{}, fmt.Errorf("the latest master build is not published under %s", MasterTag)
	}
	for _, a := range body.Assets {
		m := masterArchive.FindStringSubmatch(a.Name)
		if m == nil {
			continue
		}
		v := Canonical(m[1])
		if !IsMasterBuild(v) {
			return Release{}, fmt.Errorf("master build archive %q does not carry a master version", a.Name)
		}
		rel := Release{Version: v, URL: strings.TrimSpace(body.HTMLURL)}
		if t, err := time.Parse(time.RFC3339, body.PublishedAt); err == nil {
			rel.PublishedAt = t.UTC()
		}
		if rel.URL == "" {
			rel.URL = fmt.Sprintf("https://github.com/%s/releases/tag/%s", Repo, MasterTag)
		}
		return rel, nil
	}
	return Release{}, fmt.Errorf("the latest master build has no archives yet")
}

// IsMasterBuild reports whether v is a master build's version: a semantic
// version whose prerelease starts with "master.".
func IsMasterBuild(v string) bool {
	return strings.HasPrefix(semver.Prerelease(Canonical(v)), "-master.")
}

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

// describe matches what `git describe --tags --always --dirty` prints at or
// past a release tag: the tag, then `-<commits>-g<hash>` when HEAD is past
// it, then `-dirty` for uncommitted changes.
var describe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-(\d+)-g([0-9a-f]+))?(-dirty)?$`)

// describedVersion is v, canonical, with a `git describe` stamp placed where
// the build really is on the version line. On the tag — clean or dirty — it
// is that release. Past it, it is the next patch's lowest prerelease,
// `v0.13.1-0.46.gabc1234`, which sorts above the tag and below every release
// after it, as a Go pseudo-version does. Anything else is Canonical's.
func describedVersion(v string) string {
	m := describe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return Canonical(v)
	}
	if m[4] == "" {
		return Canonical("v" + m[1] + "." + m[2] + "." + m[3])
	}
	patch, err := strconv.Atoi(m[3])
	if err != nil {
		return ""
	}
	return Canonical(fmt.Sprintf("v%s.%s.%d-0.%s.g%s", m[1], m[2], patch+1, m[4], m[5]))
}

// IsNewer reports whether latest is a strictly newer stable version than
// current.
//
// A `dev` build — plain `go build`, version.Version()'s fallback — is never
// behind. It has no place on the version line: it may be newer than every
// release or older than all of them, and telling a developer building from
// source to download a release is noise at best and wrong at worst.
//
// A source build stamped by `git describe --tags --always --dirty` — what
// the mage build injects — is read as what it is rather than as semver
// would read it (describedVersion): `v0.13.0-46-gabc1234` is 46 commits
// past v0.13.0, not a prerelease below it, so it is behind the next release
// and never behind v0.13.0 itself.
func IsNewer(current, latest string) bool {
	c, l := describedVersion(current), Canonical(latest)
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
