package api

import (
	"encoding/json"
	"net/http"
)

// Stable error codes (spec §13.1). The vocabulary grows additively as
// endpoints land; codes never change meaning.
const (
	CodeUnauthorized     = "unauthorized"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeInvalidJSON      = "invalid_json"
	CodeValidationFailed = "validation_failed"
	CodeInvalidState     = "invalid_state"
	CodeInternal         = "internal"
)

// errorBody is the §13.1 error envelope:
// {"error": {"code", "message", "details"?}}.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details carries values a client must branch on rather than parse out
	// of Message — the current state of a 409, for instance (§13.1). Omitted
	// when empty, so responses without structured detail are unchanged.
	Details map[string]string `json:"details,omitempty"`
}

// writeJSON writes v as the JSON response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoding a value we constructed can only fail on a broken connection;
	// there is no useful recovery at this point.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the error envelope with the given status and stable code.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// writeConflict writes a 409 carrying structured details — the current state
// of a rejected transition, or the reason a precondition failed (§13.1).
// Conflicts are the only responses in v1 that clients branch on.
func writeConflict(w http.ResponseWriter, code, message string, details map[string]string) {
	writeJSON(w, http.StatusConflict,
		errorBody{Error: errorDetail{Code: code, Message: message, Details: details}})
}
