package main

import (
	"bufio"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// minVersion is the oldest release the site publishes (task 131): the first
// tag whose Pages build the site's current layout family can be assembled
// over.
var minVersion = semver{0, 6, 0}

type semver [3]int

func (a semver) less(b semver) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// releaseTag matches a release tag exactly. A prerelease (`v0.1.0-rc1`)
// carries a `-` and so never matches: it is never published.
var releaseTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// parseTag reports the version a release tag names, and false for anything
// else — a prerelease, a non-tag directory name, `latest`, `dev`.
func parseTag(tag string) (semver, bool) {
	m := releaseTag.FindStringSubmatch(tag)
	if m == nil {
		return semver{}, false
	}
	var v semver
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return semver{}, false
		}
		v[i] = n
	}
	return v, true
}

// filterTags returns the release tags the site publishes, newest first:
// release tags (no prerelease suffix) at or above minVersion, deduplicated
// and ordered by semver rather than by string, so v0.10.1 sorts above v0.9.0.
func filterTags(tags []string) []string {
	seen := map[string]semver{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		v, ok := parseTag(t)
		if !ok || v.less(minVersion) {
			continue
		}
		seen[t] = v
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return seen[out[j]].less(seen[out[i]]) })
	return out
}

func readLines(r io.Reader) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}
