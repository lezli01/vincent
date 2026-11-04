package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultTimeout bounds one request when New is given no *http.Client.
const defaultTimeout = 30 * time.Second

// maxResponseBytes bounds a response body: a search page of 100 issues with
// rendered descriptions is the largest answer read.
const maxResponseBytes = 32 << 20

// Client is one Jira Cloud site's REST v3 client. It holds no credential:
// Creds is called on every request and its answer never cached. Build it with
// New; a zero auth or pager is the Cloud implementation.
type Client struct {
	// BaseURL is the site's normalized root, `https://acme.atlassian.net`.
	BaseURL string
	// HTTP sends every request; nil is a client with defaultTimeout.
	HTTP *http.Client
	// Creds answers the user (the account's email) and API token.
	Creds func() (user, token string, err error)

	// auth and pager are the edition seam (task 142 decision 7).
	auth  authenticator
	pager pager
	// now stamps RetryAt; time.Now when nil.
	now func() time.Time
}

// New is a Cloud client for site, the binding's JiraSite: a bare host gets
// https://, and a trailing slash and any userinfo are dropped. A nil httpc is
// a client with a 30 s timeout.
func New(site string, httpc *http.Client, creds func() (user, token string, err error)) *Client {
	if httpc == nil {
		httpc = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		BaseURL: normalizeSite(site),
		HTTP:    httpc,
		Creds:   creds,
		auth:    cloudBasicAuth{},
		pager:   cloudTokenPager{},
	}
}

func normalizeSite(site string) string {
	site = strings.TrimSpace(site)
	if !strings.Contains(site, "://") {
		site = "https://" + site
	}
	if u, err := url.Parse(site); err == nil {
		u.User = nil
		u.RawQuery, u.Fragment = "", ""
		site = u.String()
	}
	return strings.TrimRight(site, "/")
}

// Site is the normalized base URL.
func (c *Client) Site() string { return c.BaseURL }

// BrowseURL is an issue's page on the site: BaseURL/browse/KEY.
func (c *Client) BrowseURL(key string) string {
	return c.BaseURL + "/browse/" + url.PathEscape(key)
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: defaultTimeout}
}

func (c *Client) authenticator() authenticator {
	if c.auth != nil {
		return c.auth
	}
	return cloudBasicAuth{}
}

func (c *Client) searchPager() pager {
	if c.pager != nil {
		return c.pager
	}
	return cloudTokenPager{}
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// credentials calls Creds. A no_credential *Error passes through unchanged;
// any other failure becomes one.
func (c *Client) credentials(op string) (user, token string, err error) {
	if c.Creds == nil {
		return "", "", &Error{Op: op, Reason: ReasonNoCredential, Err: errors.New("no credential source")}
	}
	user, token, err = c.Creds()
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Reason == ReasonNoCredential {
			return "", "", err
		}
		return "", "", &Error{Op: op, Reason: ReasonNoCredential, Err: err}
	}
	if user == "" || token == "" {
		return "", "", &Error{Op: op, Reason: ReasonNoCredential, Err: errors.New("the credential source answered an empty user or token")}
	}
	return user, token, nil
}

// do sends one request and decodes a 2xx answer into out (when non-nil). It
// never retries: the caller's policy decides, and a repeated 401 or 403 is
// how Jira's CAPTCHA gets tripped.
func (c *Client) do(ctx context.Context, op, method, path string, query url.Values, body, out any) error {
	user, token, err := c.credentials(op)
	if err != nil {
		return err
	}
	secrets := secretsOf(user, token)
	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return &Error{Op: op, Reason: ReasonBadRequest, Err: errors.New(scrub(err.Error(), secrets))}
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return &Error{Op: op, Reason: ReasonBadRequest, Err: err}
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return &Error{Op: op, Reason: ReasonBadRequest, Err: errors.New(scrub(err.Error(), secrets))}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "vincent")
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.authenticator().authenticate(req, user, token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return transportError(op, err, u, secrets)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return transportError(op, err, u, secrets)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(op, resp, raw, c.clock(), secrets)
	}
	if len(raw) > maxResponseBytes {
		return &Error{Op: op, Status: resp.StatusCode, Reason: ReasonBadResponse, Err: fmt.Errorf("answer larger than %d bytes", maxResponseBytes)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Op: op, Status: resp.StatusCode, Reason: ReasonBadResponse, Err: fmt.Errorf("decode: %w", err)}
	}
	return nil
}
