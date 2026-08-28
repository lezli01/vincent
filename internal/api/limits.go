package api

import (
	"fmt"
	"mime"
	"net/http"
	"strings"
)

// Request bounds (§13.1, amended 2026-08-25 for issue #140). They are fixed
// constants rather than configuration, following §5.2's workflow-source bound:
// a body larger than these is a buggy or confused client, not a workload to
// tune for. The daemon is the only writer of every resource behind them, so a
// body it refuses costs a caller nothing it could not resend smaller.
const (
	// maxRequestBytes bounds an ordinary request body: ids, names, paths,
	// flags. Everything the API accepts on these routes is a short scalar a
	// human typed or a client copied out of a previous response.
	maxRequestBytes = 64 << 10
	// maxLargeRequestBytes bounds the routes that legitimately carry something
	// written at length — a workflow source (§5.2 fixes one at
	// workflow.MaxSourceBytes) or an agent prompt. It sits well above that 1
	// MiB because the artifact travels as a JSON string, where every newline
	// and quote costs a second byte.
	maxLargeRequestBytes = 4 << 20
)

// Per-field bounds. The body bound above stops the daemon reading an
// unbounded stream; these stop one field of a within-bound body from being the
// whole of it, because these values are persisted, rendered into prompts and
// handed to subprocesses long after the request is answered.
const (
	maxNameBytes        = 512      // project name, branch name or override
	maxTitleBytes       = 1 << 10  // task title
	maxDescriptionBytes = 64 << 10 // task description
	maxPromptBytes      = 1 << 20  // repair prompt, retry prompt override
	maxCommandBytes     = 16 << 10 // retry run override — one shell command
	maxFieldKeyBytes    = 256      // one fields key
	maxAnswersKeyBytes  = 64 << 10 // one answers key — the agent's question text
	maxFieldValueBytes  = 64 << 10 // one fields/answers value
	maxFieldCount       = 100      // fields/answers entries in one request
	maxValueCount       = 100      // values in one answer
	// maxIdempotencyKeyBytes bounds the Idempotency-Key header (task 040). A
	// header rather than a body field, but persisted and compared byte for
	// byte on a later request like any other, so it is bounded alongside them.
	// 255 B is the size the header is conventionally given elsewhere and is
	// generous for the UUID a client actually sends.
	maxIdempotencyKeyBytes = 255
)

// The two key bounds differ because the two keys are different kinds of thing
// (amended 2026-08-26, issue #197). A `fields` key is a caller-chosen
// identifier a human or a workflow author types (§8.1.2), and 256 B is generous
// for one. An `answers` key is not chosen by the caller at all: it is the
// agent's verbatim question text, which §7.4 makes the lookup key and §9.2
// writes back to the CLI unchanged, so nothing between the agent and the
// answer route may shorten it. Bounding it like an identifier made any question
// longer than 256 B unanswerable — the daemon parked on, persisted and rendered
// a question it then refused every answer to. It is bounded like the prose it
// is, at the same size as the answer value it arrives beside.

// boundString reports the §13.1 validation message for a field longer than
// limit, and "" when it fits. The bound is in bytes rather than runes: what it exists
// to cap is what the daemon stores, templates into a prompt and passes to a
// subprocess, and those are all counted in bytes.
func boundString(field, v string, limit int) string {
	if len(v) <= limit {
		return ""
	}
	return fmt.Sprintf("%s must be at most %d bytes (got %d)", field, limit, len(v))
}

// boundMap bounds an object field: how many entries it carries, and how long
// one key and one value may be. The message names the field and the limit and
// never echoes the value — a bound is reported, not the body that broke it.
func boundMap(field string, m map[string]string, maxEntries, maxKey, maxValue int) string {
	if msg := boundCount(field, len(m), maxEntries); msg != "" {
		return msg
	}
	for k, v := range m {
		if msg := boundString(field+" key", k, maxKey); msg != "" {
			return msg
		}
		if msg := boundString(fmt.Sprintf("%s[%s]", field, truncKey(k)), v, maxValue); msg != "" {
			return msg
		}
	}
	return ""
}

// boundCount reports the message for a map or array carrying more than limit
// entries.
func boundCount(field string, n, limit int) string {
	if n <= limit {
		return ""
	}
	return fmt.Sprintf("%s must have at most %d entries (got %d)", field, limit, n)
}

// truncKey keeps a key readable in an error message without letting an
// oversized one become the message.
func truncKey(k string) string {
	if len(k) <= 64 {
		return k
	}
	return k[:64] + "…"
}

// boundTaskFields bounds the inputs a task carries from its creation into
// every prompt it renders: the title, the optional description and the open
// `fields` map (§8.1.2). POST /v1/tasks and POST /v1/resolve take the same
// inputs and bound them identically — a draft that resolves has to be one that
// can then be created.
func boundTaskFields(title, description string, fields map[string]string) string {
	if msg := boundString("title", title, maxTitleBytes); msg != "" {
		return msg
	}
	if msg := boundString("description", description, maxDescriptionBytes); msg != "" {
		return msg
	}
	return boundMap("fields", fields, maxFieldCount, maxFieldKeyBytes, maxFieldValueBytes)
}

// checkContentType applies §13.1's lenient content-type rule. An absent or
// empty header is accepted — a body with no label is decoded and stands or
// falls as JSON — as is any JSON media type, parameters and all
// (`application/json; charset=utf-8`, `application/merge-patch+json`). Only a
// body explicitly labelled something else is refused: `text/html`, or the
// `application/x-www-form-urlencoded` that a plain `curl -d` sends, which is
// the confused client this catches. Every in-repo client already sends
// `application/json`, so nothing legitimate pays for it.
func checkContentType(w http.ResponseWriter, r *http.Request) bool {
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return true
	}
	if mt, _, err := mime.ParseMediaType(raw); err == nil && isJSONMediaType(mt) {
		return true
	}
	writeError(w, http.StatusUnsupportedMediaType, CodeUnsupportedMediaType,
		fmt.Sprintf("Content-Type %q is not JSON; send application/json", raw))
	return false
}

// isJSONMediaType reports whether mt is a JSON media type: `*/json` or one of
// the structured `+json` suffixes.
func isJSONMediaType(mt string) bool {
	return strings.HasSuffix(mt, "/json") || strings.HasSuffix(mt, "+json")
}
