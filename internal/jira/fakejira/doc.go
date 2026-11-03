// Package fakejira is the fake Jira Cloud's corpus and REST answers (issue
// #907, part of #896). It is **test infrastructure**: nothing in the daemon
// imports it, and nothing it says is a claim about vincent's own behaviour —
// only about what Jira Cloud answers.
//
// It exists as a package rather than inside cmd/fakejira because the fake has
// two legs that must agree: a test mounts a *Store behind httptest.NewServer,
// and cmd/fakejira — the stand-alone server the gate (#914) and the e2e test
// drive — mounts the very same handler behind a real listener. Unlike
// internal/github/fakeissues there is no CLI leg to translate for, so a Store
// is itself the http.Handler both legs serve (decision 4). It cannot live in
// internal/jira/jiratest, whose contract is that only _test files import it —
// cmd/fakejira is a main.
//
// What it holds:
//
//   - The corpus: one JSON file (Store.Path, cmd/fakejira's --corpus) holding
//     the site, its projects — each with a workflow graph of statuses, their
//     statusCategory key (`new`, `indeterminate`, `done`, the real keys and
//     never the OpenAPI examples' `in-flight`/`completed`), and transitions
//     whose screen fields carry `required` and `allowedValues` — the issues
//     (a stable `id`, the current `key`, `previousKeys`, fields, `updated` at
//     millisecond precision, a resolution) and the comments (an optional
//     `visibility`). A description and a comment body store the ADF document
//     and its rendered HTML side by side; the seed and the tests write both,
//     and the fake never converts Markdown (decision 3). The file is re-read
//     on every request and written back through a temp file and a rename, so
//     a running server sees an edit made between two polls. Unset, the
//     built-in corpus answers and a write is answered but not kept; set but
//     missing, the built-in corpus seeds it.
//   - The ADF renderer: a comment POSTed as ADF is rendered to HTML by a tiny
//     renderer covering exactly `doc`, `paragraph`, `text` and `hardBreak` —
//     the subset #906 emits. Any other node, and any mark, is a 400, so a
//     client that drifts from the subset fails loudly rather than agreeing
//     with the code under test by construction.
//   - The routes, all under /rest/api/3: `search/jql` (a JQL subset — see
//     ParseJQL — paged by nextPageToken and isLast with no `total`, with
//     maxResults, fields and expand=renderedFields; JQL outside the subset is
//     a 400, decision 5); `issue/{idOrKey}`, which resolves a key the issue
//     had before a move; `issue/bulkfetch`, which omits deleted and forbidden
//     ids alike; `issue/{k}/transitions`, GET listing only what the issue's
//     current status allows (with expand=transitions.fields) and POST
//     applying one, answering Jira's `errors` map for a missing required
//     screen field or a value outside its allowedValues; `issue/{k}/comment`,
//     GET paged by startAt and maxResults with `total` (Jira's comment page
//     has one) and expand=renderedBody, POST with an ADF body and an optional
//     visibility answering 201; `myself`; and `serverInfo`.
//   - The per-issue faults, set by `_fake` markers in the corpus or by the
//     mutation helpers: moved (Store.Move: a new project and key, the same
//     id, the old key still resolving), deleted (404 with Jira's
//     errorMessages body, absent from search and bulkfetch) and forbidden
//     (byte-identical to deleted from outside). `_fake` never reaches an
//     answer.
//   - The scenarios, read from Store.ScenarioFile on every request so one
//     switches without a restart: `rate-limited` (429 with Retry-After),
//     `unauthorized` (401), `read-only` (transition and comment POSTs 403),
//     `unreachable` (the connection is hijacked and closed — no HTTP answer
//     at all) and `search-lag N` (after a write, the next N search calls
//     answer each written issue's pre-write status and updated; the count
//     lives in the corpus's `_fake` state, so it survives between requests).
//   - Auth: every route but the control route wants `Authorization: Basic
//     base64(user:token)` matching Store.User and Store.Token (DefaultUser
//     and DefaultToken when unset); anything else is a 401.
//   - The mutation helpers, for a test that changes Jira between two syncs:
//     Move, Delete, Forbid, AddComment, EditComment, RemoveComment,
//     Transition and SetUpdated, over the general Update. Each needs a Store
//     with a Path (ErrNoCorpusFile otherwise). They are not writes in the
//     search-lag sense: only a REST write arms the lag.
//   - The request log: with Store.RequestLog set, every request appends
//     `METHOD path status`, flushed per request, and CountRequests counts
//     them as fakeissues' does. An unreachable answer records status 0.
//   - The control route: `POST /_fake/shutdown`, served only to a loopback
//     peer and without Jira auth, calls Store.OnShutdown. It is the one way
//     cmd/fakejira stops cleanly on every OS (decision 1).
//
// Response shapes are built from Atlassian's documented v3 shapes and the
// jira-model research; a comment on each names its source. #899 later
// reshapes them against captured fixtures (decision 2).
package fakejira
