package jira

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The failure reasons. The first six are internal/issuesource's Reason*
// strings, so the Jira source passes them through unchanged.
const (
	ReasonUnauthorized = "unauthorized"
	ReasonForbidden    = "forbidden"
	ReasonNotFound     = "not_found"
	ReasonRateLimited  = "rate_limited"
	ReasonUnreachable  = "unreachable"
	ReasonNoCredential = "no_credential" //nolint:gosec // G101: a reason, not a credential
	ReasonBadRequest   = "bad_request"
	ReasonBadResponse  = "bad_response"
)

// Error is every failure the client returns. Its Error() never carries the
// API token or the Authorization header: what reaches Err has been scrubbed
// of both before it is wrapped.
type Error struct {
	// Op names the call: "search", "get issue", …
	Op string
	// Status is the HTTP status, 0 when there was no answer.
	Status int
	// Reason is one of the Reason* constants.
	Reason string
	// RetryAt is when a rate_limited caller may try again; zero when Jira
	// said nothing.
	RetryAt time.Time
	// Err is the cause: Jira's errorMessages, a decode error, a transport
	// error.
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("jira: ")
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	b.WriteString(e.Reason)
	if e.Status != 0 {
		fmt.Fprintf(&b, " (HTTP %d)", e.Status)
	}
	if !e.RetryAt.IsZero() {
		fmt.Fprintf(&b, ", retry at %s", e.RetryAt.UTC().Format(time.RFC3339))
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// ReasonOf is err's Reason: the *Error's via errors.As, ReasonUnreachable for
// a transport error not wrapped as one, and "" for nil or anything else.
func ReasonOf(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	var ue *url.Error
	var ne net.Error
	if errors.As(err, &ue) || errors.As(err, &ne) {
		return ReasonUnreachable
	}
	return ""
}

// reasonForStatus maps a non-2xx answer. A 5xx is the server not answering
// usefully, which is unreachable to a caller; any other 4xx is bad_request.
func reasonForStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return ReasonUnauthorized
	case status == http.StatusForbidden:
		return ReasonForbidden
	case status == http.StatusNotFound:
		return ReasonNotFound
	case status == http.StatusTooManyRequests:
		return ReasonRateLimited
	case status >= 500:
		return ReasonUnreachable
	default:
		return ReasonBadRequest
	}
}

// maxErrorText bounds the errorMessages carried into an Error.
const maxErrorText = 500

// statusError builds the *Error for a non-2xx answer.
func statusError(op string, resp *http.Response, body []byte, now time.Time, secrets []string) *Error {
	e := &Error{Op: op, Status: resp.StatusCode, Reason: reasonForStatus(resp.StatusCode)}
	msg := errorText(body)
	if resp.StatusCode == http.StatusForbidden && captcha(resp.Header) {
		msg = strings.TrimSpace("CAPTCHA challenge: sign in to Jira in a browser once to clear it. " + msg)
	}
	if msg != "" {
		e.Err = errors.New(scrub(msg, secrets))
	}
	if e.Reason == ReasonRateLimited {
		e.RetryAt = retryAt(resp.Header, now)
	}
	return e
}

// captcha reports Jira's CAPTCHA answer to a login: 403 with
// X-Seraph-LoginReason AUTHENTICATION_DENIED (or the
// X-Authentication-Denied-Reason header).
func captcha(h http.Header) bool {
	return strings.Contains(strings.ToUpper(h.Get("X-Seraph-LoginReason")), "AUTHENTICATION_DENIED") ||
		h.Get("X-Authentication-Denied-Reason") != ""
}

// errorText is Jira's error envelope, `{"errorMessages": [...], "errors":
// {...}}`, flattened; or the body's first line when it is not one.
func errorText(body []byte) string {
	var env struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	var parts []string
	if json.Unmarshal(body, &env) == nil {
		parts = append(parts, env.ErrorMessages...)
		for k, v := range env.Errors {
			parts = append(parts, k+": "+v)
		}
	} else if line, _, _ := strings.Cut(strings.TrimSpace(string(body)), "\n"); line != "" {
		parts = append(parts, line)
	}
	msg := strings.Join(parts, "; ")
	if len(msg) > maxErrorText {
		msg = msg[:maxErrorText] + "…"
	}
	return msg
}

// resetLayouts are the X-RateLimit-Reset shapes Atlassian documents (ISO
// 8601, with or without seconds).
var resetLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00", "2006-01-02T15:04:05.000Z0700"}

// retryAt reads a 429's Retry-After — seconds, or an HTTP date — falling back
// to X-RateLimit-Reset. Zero when neither parses.
func retryAt(h http.Header, now time.Time) time.Time {
	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
			return now.Add(time.Duration(secs) * time.Second)
		}
		if t, err := http.ParseTime(ra); err == nil {
			return t
		}
	}
	if rs := strings.TrimSpace(h.Get("X-RateLimit-Reset")); rs != "" {
		for _, layout := range resetLayouts {
			if t, err := time.Parse(layout, rs); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// secretsOf is everything that must never reach an error string for one
// request: the token, and the Authorization header's value and its base64
// payload.
func secretsOf(user, token string) []string {
	if token == "" {
		return nil
	}
	payload := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
	return []string{"Basic " + payload, payload, token}
}

// scrub replaces every secret in s.
func scrub(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	return s
}

// redactedError carries a scrubbed message. It does not Unwrap — the cause's
// own text might carry a secret — but answers errors.Is for the context
// errors a caller tests for.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }

func (e *redactedError) Is(target error) bool {
	return errors.Is(e.cause, target)
}

// transportError wraps a failed round trip as unreachable. A *url.Error is
// rebuilt around the request's redacted URL, and the whole text scrubbed.
func transportError(op string, err error, u *url.URL, secrets []string) *Error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = &url.Error{Op: ue.Op, URL: redactURL(u), Err: ue.Err}
	}
	return &Error{Op: op, Reason: ReasonUnreachable, Err: &redactedError{msg: scrub(err.Error(), secrets), cause: err}}
}

// redactURL is u without userinfo or query: a JQL query is no secret, but an
// error line is no place for one either.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.User = nil
	c.RawQuery = ""
	c.ForceQuery = false
	return c.String()
}
