package mcp

import (
	"encoding/json"
	"net/http"
)

// The `config_get` tool is the one place this package changes what a route
// says (task 060 decision 3).
//
// GET /v1/config serves config.yaml in full, values included: over HTTP that
// is loopback behind an 0600 bearer token, the same trust boundary as the 0600
// file itself. An MCP tool call is not that. It is replayed on behalf of an
// agent step, and whatever comes back lands in the model's context and in the
// step's transcript — so serving the literal `environment.set` values and the
// `notify.command` argv here would put a webhook URL or an API key somewhere
// nobody chose to put it.
//
// The two fields are masked; nothing else is. Names survive, because "which
// variables are set" is the question an agent has any business asking, and it
// is the same line §12.3 already draws for the log. A test asserts the HTTP
// body and the MCP body differ in exactly these fields and nowhere else.

// redactedMask replaces a value the MCP rendering will not disclose.
const redactedMask = "[redacted]"

// redactBody returns the tool-visible rendering of one route's response body.
// Every route but config_get is passed through untouched.
func redactBody(r Route, status int, body []byte) []byte {
	if r.Tool != "config_get" || r.Method != http.MethodGet || status >= http.StatusBadRequest {
		return body
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		// An unparseable body is not one this function understands well enough
		// to redact, and passing it through would leak exactly what it exists
		// to hide. Refuse instead.
		return []byte(`{"error":{"code":"internal","message":"configuration could not be redacted"}}`)
	}
	if raw, ok := doc["environment"]; ok {
		doc["environment"] = redactEnvironment(raw)
	}
	if raw, ok := doc["notify"]; ok {
		doc["notify"] = redactNotify(raw)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return []byte(`{"error":{"code":"internal","message":"configuration could not be redacted"}}`)
	}
	return out
}

// redactEnvironment masks the values under `environment.set`, keeping its
// names and leaving `inherit` and `unset` — both name-only already — alone.
func redactEnvironment(raw json.RawMessage) json.RawMessage {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return raw
	}
	setRaw, ok := env["set"]
	if !ok {
		return raw
	}
	var set map[string]string
	if err := json.Unmarshal(setRaw, &set); err != nil {
		return raw
	}
	for k := range set {
		set[k] = redactedMask
	}
	b, err := json.Marshal(set)
	if err != nil {
		return raw
	}
	env["set"] = b
	out, err := json.Marshal(env)
	if err != nil {
		return raw
	}
	return out
}

// redactNotify masks every element of `notify.command`. The argv is masked
// whole rather than from the first argument on: the secret in the case that
// motivated this is a webhook URL, and it is an argument, not the program.
// `notify.on` is a list of §6 state names and stays.
func redactNotify(raw json.RawMessage) json.RawMessage {
	var n map[string]json.RawMessage
	if err := json.Unmarshal(raw, &n); err != nil {
		return raw
	}
	cmdRaw, ok := n["command"]
	if !ok {
		return raw
	}
	var cmd []string
	if err := json.Unmarshal(cmdRaw, &cmd); err != nil {
		return raw
	}
	for i := range cmd {
		cmd[i] = redactedMask
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return raw
	}
	n["command"] = b
	out, err := json.Marshal(n)
	if err != nil {
		return raw
	}
	return out
}
