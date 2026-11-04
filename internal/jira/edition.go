package jira

import (
	"net/http"
	"net/url"
)

// The edition seam (task 142 decision 7). Cloud and Data Center differ in how
// a request authenticates and how a search pages; everything else this
// client reads is the same REST shape. Each difference is one tiny
// interface, and New wires the Cloud implementation of both. A Data Center
// leg — out of scope here — would add a bearer-PAT authenticator and a
// startAt pager beside them.

// authenticator puts the credential on one request.
type authenticator interface {
	authenticate(req *http.Request, user, token string)
}

// pager drives one search's pages. first sets the first page's paging
// parameters; next reads a page's continuation into q and reports whether
// there is another page. The continuation lives in q, which is local to one
// Search call, and so is never stored beyond it.
type pager interface {
	first(q url.Values)
	next(page *searchPage, q url.Values) bool
}

// cloudBasicAuth is Cloud's `Authorization: Basic base64(email:api_token)`.
type cloudBasicAuth struct{}

func (cloudBasicAuth) authenticate(req *http.Request, user, token string) {
	req.SetBasicAuth(user, token)
}

// cloudTokenPager is search/jql's nextPageToken paging: no total, and the
// token is absent on the last page.
type cloudTokenPager struct{}

func (cloudTokenPager) first(q url.Values) {
	q.Del("nextPageToken")
}

func (cloudTokenPager) next(page *searchPage, q url.Values) bool {
	if page.NextPageToken == "" {
		return false
	}
	q.Set("nextPageToken", page.NextPageToken)
	return true
}
