package fakejira

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// The request log is how a test counts what a sync asked Jira for, path by
// path, the way fakeissues' does. It is a file rather than a counter in
// memory so the cmd/fakejira leg, another process, serves it the same way.
//
// A line is `METHOD path status`: `GET rest/api/3/search/jql 200`. The path
// has no query and no leading slash. A dropped connection records 0. Each
// line is appended and the file closed per request, so a reader — or a
// server stopping — never loses one.

func (s *Store) logRequest(method, path string, status int) error {
	if s.RequestLog == "" {
		return nil
	}
	f, err := os.OpenFile(s.RequestLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s %s %d\n", strings.ToUpper(method), strings.TrimPrefix(path, "/"), status)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// LoggedRequest is one line of a request log.
type LoggedRequest struct {
	Method string
	Path   string
	Status int
}

// Requests reads a request log, oldest first. A log never written is empty,
// not an error.
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
			return nil, fmt.Errorf("fakejira: %s: %q: %w", log, sc.Text(), err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// CountRequests counts the requests in log for method and path — without a
// query, `rest/api/3/search/jql` — and, when status is not 0, only those
// answered with it.
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
