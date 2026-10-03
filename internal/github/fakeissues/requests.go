package fakeissues

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
)

// The request log is how a test counts what a sync asked GitHub for, path
// by path (task 130 decision 24, 130.16): the issue listing and the comment
// listing are revalidated separately, and "the comment listing was asked
// once and answered 304" is not something the corpus can show. It is a file
// rather than a counter in memory because cmd/fakegh is one process per
// `gh api` call; one line per request, appended, serves that leg and the
// net/http one alike.
//
// A line is `METHOD path status`: `GET repos/octo/repo/issues/comments 304`.
// The path has no query and no leading slash, so a test matches the
// endpoint without restating per_page or since. An unreachable answer has
// no status and records 0.

func (s Store) logRequest(req Request, resp Response) error {
	if s.RequestLog == "" {
		return nil
	}
	f, err := os.OpenFile(s.RequestLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s %s %d\n", requestMethod(req), requestPath(req.Endpoint), resp.Status)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func requestMethod(req Request) string {
	if req.Method == "" {
		return "GET"
	}
	return strings.ToUpper(req.Method)
}

func requestPath(endpoint string) string {
	p, _, _ := strings.Cut(strings.TrimPrefix(endpoint, "/"), "?")
	if u, err := url.PathUnescape(p); err == nil {
		return u
	}
	return p
}

// LoggedRequest is one line of a request log.
type LoggedRequest struct {
	Method string
	Path   string
	Status int
}

// Requests reads a request log, oldest first. A log never written is empty,
// not an error: no request reached the fake.
func Requests(log string) ([]LoggedRequest, error) {
	f, err := os.Open(log)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []LoggedRequest
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r LoggedRequest
		if _, err := fmt.Sscanf(strings.TrimRight(sc.Text(), "\r"), "%s %s %d", &r.Method, &r.Path, &r.Status); err != nil {
			return nil, fmt.Errorf("fakeissues: %s: %q: %w", log, sc.Text(), err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// CountRequests counts the requests in log for method and path — the path
// without a query, `repos/octo/repo/issues/comments` — and, when status is
// not 0, only those answered with it: CountRequests(log, "GET",
// "repos/octo/repo/issues/comments", 304) is the conditional hits.
func CountRequests(log, method, path string, status int) (int, error) {
	reqs, err := Requests(log)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range reqs {
		if r.Method == strings.ToUpper(method) && r.Path == strings.TrimPrefix(path, "/") && (status == 0 || r.Status == status) {
			n++
		}
	}
	return n, nil
}
