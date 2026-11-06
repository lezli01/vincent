// Package jira is vincent's native Jira Cloud REST v3 client: both sides of
// the Jira issue source of spec §5.6 — the reads the importer makes (task
// 142.12, #908) and the writes the outbox's drain makes (task 142.13,
// #909). It is
// hand-written net/http, the way internal/github/rest.go is — no SDK
// (task 142 decision 1) — and it imports the standard library and
// internal/jira/jiramap only, never internal/issuesource, internal/store or
// internal/config, so the source and the outbox can both build on it without
// a cycle; a test walks `go list -deps` to hold that.
//
// # Writes (task 142.13)
//
// write.go is the write half, three calls and nothing more: Transitions
// lists the transitions available on an issue with their screen fields
// (expand=transitions.fields), decoded into the shape
// jiramap.ChooseTransition reads; DoTransition performs one, setting the
// resolution on its screen only when one is given; AddComment posts an
// Atlassian Document Format body and returns Jira's comment id. Which
// transition to take is jiramap's decision, never this package's. A 403 on
// a write is ReasonNoWriteScope rather than the read path's ReasonForbidden
// (decision 3), so the drain fails the row terminally the way it fails
// GitHub's missing write scope.
//
// # Credentials (task 142 decision 4, spec §12.3)
//
// Cloud authenticates with `Authorization: Basic base64(email:api_token)`.
// The client holds no credential: Client.Creds is called on every request
// and its answer is never cached, so a rotated token in the environment the
// binding names (`jira.user_env`, `jira.token_env`) is picked up on the next
// call. EnvCredentials resolves those two names in a K=V environment. A
// Creds answering a no_credential *Error is returned unchanged and no
// request is sent. The token and the Authorization header never appear in an
// error string: transport errors are rebuilt without the URL's userinfo and
// scrubbed of both before they are wrapped.
//
// # Endpoints and paging (task 142 decision 5)
//
// Every path is pinned in endpoints.go. Listing is GET
// /rest/api/3/search/jql alone — the old /rest/api/3/search was removed
// upstream (CHANGE-2046) and is never called. Its `fields` default is the id
// alone, so every issue read names IssueFieldNames explicitly and asks for
// expand=renderedFields. The page has no `total`; nextPageToken is absent on
// the last page, and is followed only within one Search call — it is never
// returned or stored, so a sync's cursor is the JQL InitialJQL and
// IncrementalJQL build (both ordered by id and resumed by id > N, the second
// also bounded by updated >= since less Overlap), not a token that expires.
//
// # Errors
//
// Every failure is an *Error whose Reason is one of the Reason* strings; the
// first six equal internal/issuesource's. 401 is unauthorized, 403 forbidden
// (a CAPTCHA challenge or a permission), 404 not_found, 429 rate_limited with
// RetryAt from Retry-After (seconds or an HTTP date) or else X-RateLimit-Reset
// (ISO 8601), 400 bad_request, a transport failure or a 5xx unreachable, and
// an undecodable body bad_response. The client never retries anything — not
// 401 or 403 above all, since repeated failed logins trip Jira's CAPTCHA —
// and leaves the policy to its caller.
//
// # Editions (task 142 decision 7)
//
// Authentication and search paging sit behind two unexported interfaces,
// the edition seam a later Data Center leg (a bearer PAT, startAt paging)
// fills in; New wires the Cloud implementations.
//
// # Mapping (task 142 decision 9)
//
// IssueFields embeds jiramap.Fields, so jiramap.FromJira, Labels, KindSeed
// and RemoteJSON take an Issue's .Fields.Fields directly; what an issue's
// status means is jiramap's answer, never this package's.
package jira
