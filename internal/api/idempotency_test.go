package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// postTaskKey is doJSON against POST /v1/tasks with an Idempotency-Key header.
// It sets the header only when key is non-empty, so the same helper covers the
// no-header path a request without replay protection takes.
func (h *projectHarness) postTaskKey(t *testing.T, body any, key string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, h.ts.URL+"/v1/tasks", rd)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("post task: %v", err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, out
}

// rowCount reads one table's row count straight from the schema enumeration
// doctor uses, which is the only assertion that can say "no *second* task was
// created" without trusting the response that claims so.
func (h *projectHarness) rowCount(t *testing.T, table string) int64 {
	t.Helper()
	rows, err := h.store.TableRows(t.Context())
	if err != nil {
		t.Fatalf("table rows: %v", err)
	}
	return rows[table]
}

// decodeCreated parses a create response, failing the test on a status that is
// not 201.
func decodeCreated(t *testing.T, resp *http.Response, body []byte) taskResponse {
	t.Helper()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task: %d %s", resp.StatusCode, body)
	}
	var tr taskResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatalf("task body: %v (%s)", err, body)
	}
	return tr
}

// createWithKey posts a create carrying key and expects a 201.
func (h *projectHarness) createWithKey(t *testing.T, body any, key string) taskResponse {
	t.Helper()
	resp, out := h.postTaskKey(t, body, key)
	return decodeCreated(t, resp, out)
}

// TestCreateWithoutKeyIsUnchanged is the regression that matters most: a
// request carrying no Idempotency-Key behaves exactly as it did before replay
// protection existed, so two identical sends are two tasks — which is what a
// human pressing enter twice means (decision 3, task 040).
func TestCreateWithoutKeyIsUnchanged(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	body := map[string]any{"project_id": h.projectID, "title": "no key here"}

	first := h.createWithKey(t, body, "")
	second := h.createWithKey(t, body, "")

	if first.ID == second.ID {
		t.Errorf("two keyless creates returned the same task %d", first.ID)
	}
	if n := h.rowCount(t, "tasks"); n != 2 {
		t.Errorf("tasks = %d, want 2 from two keyless creates", n)
	}
	if n := h.rowCount(t, "idempotency_keys"); n != 0 {
		t.Errorf("idempotency_keys = %d after keyless creates, want 0", n)
	}
}

// TestSameKeySameBodyReplays covers the case the whole feature exists for: the
// client committed a task, lost the response, and re-sent. It gets a 201
// naming the task that already exists, not a second one.
func TestSameKeySameBodyReplays(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	body := map[string]any{"project_id": h.projectID, "title": "replay me"}

	first := h.createWithKey(t, body, "key-1")
	second := h.createWithKey(t, body, "key-1")

	if first.ID != second.ID {
		t.Errorf("replay returned task %d, want the original %d", second.ID, first.ID)
	}
	if second.BranchName != first.BranchName || second.Title != first.Title {
		t.Errorf("replay body = %+v, want the original task %+v", second, first)
	}
	if n := h.rowCount(t, "tasks"); n != 1 {
		t.Errorf("tasks = %d after a replayed create, want 1", n)
	}
	if n := h.rowCount(t, "idempotency_keys"); n != 1 {
		t.Errorf("idempotency_keys = %d, want the one recorded key", n)
	}
}

// TestReplayIgnoresWhitespaceAndKeyOrder: the digest is over the *decoded*
// request re-marshalled canonically, so a client that reserialises its own
// body differently on the retry still replays rather than conflicting.
func TestReplayIgnoresWhitespaceAndKeyOrder(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	post := func(raw string) (*http.Response, []byte) {
		req, err := http.NewRequest(http.MethodPost, h.ts.URL+"/v1/tasks", strings.NewReader(raw))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Idempotency-Key", "key-shuffled")
		resp, err := h.ts.Client().Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		out, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp, out
	}
	resp, out := post(fmt.Sprintf(
		`{"project_id":%d,"title":"shuffled","description":"d"}`, h.projectID))
	first := decodeCreated(t, resp, out)
	resp, out = post(fmt.Sprintf(
		"{\n  \"description\": \"d\",\n  \"title\": \"shuffled\",\n  \"project_id\": %d\n}\n",
		h.projectID))
	second := decodeCreated(t, resp, out)

	if first.ID != second.ID {
		t.Errorf("reformatted retry created task %d, want a replay of %d", second.ID, first.ID)
	}
	if n := h.rowCount(t, "tasks"); n != 1 {
		t.Errorf("tasks = %d, want 1", n)
	}
}

// TestSameKeyDifferentBodyConflicts: a key reused for a second operation is a
// client bug the daemon refuses rather than papering over. The 409 keeps
// §13.1's single conflict code and carries the specific reason in details
// (decision 5).
func TestSameKeyDifferentBodyConflicts(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	first := h.createWithKey(t,
		map[string]any{"project_id": h.projectID, "title": "original"}, "key-2")

	resp, body := h.postTaskKey(t,
		map[string]any{"project_id": h.projectID, "title": "something else"}, "key-2")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("reused key with a new body: %d %s, want 409", resp.StatusCode, body)
	}
	var env errorBody
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("error body: %v (%s)", err, body)
	}
	if env.Error.Code != CodeInvalidState {
		t.Errorf("code = %q, want %q — every 409 carries one code (§13.1)",
			env.Error.Code, CodeInvalidState)
	}
	if env.Error.Details["reason"] != "idempotency_key_reused" {
		t.Errorf("details = %v, want reason=idempotency_key_reused", env.Error.Details)
	}
	if n := h.rowCount(t, "tasks"); n != 1 {
		t.Errorf("tasks = %d after a conflicting create, want just task %d", n, first.ID)
	}
}

// TestConcurrentDuplicatesCommitOne is the acceptance criterion the feature
// exists for. The store is a single WAL writer, so the two transactions
// serialize: the loser's key insert violates the primary key, its task insert
// rolls back with it, and it replays the winner's task.
func TestConcurrentDuplicatesCommitOne(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	body := map[string]any{"project_id": h.projectID, "title": "raced"}

	var wg sync.WaitGroup
	ids := make([]int64, 2)
	codes := make([]int, 2)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, out := h.postTaskKey(t, body, "key-race")
			codes[i] = resp.StatusCode
			var tr taskResponse
			if err := json.Unmarshal(out, &tr); err == nil {
				ids[i] = tr.ID
			}
		}()
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusCreated {
			t.Errorf("request %d: %d, want 201", i, code)
		}
	}
	if ids[0] != ids[1] || ids[0] == 0 {
		t.Errorf("concurrent duplicates returned tasks %d and %d, want one id", ids[0], ids[1])
	}
	if n := h.rowCount(t, "tasks"); n != 1 {
		t.Errorf("tasks = %d after two concurrent duplicates, want 1", n)
	}
}

// TestKeyBoundsAreValidated: the key is persisted and compared byte for byte
// later, so it gets the §13.1 field treatment — a 400 naming the field and the
// limit, never a silent truncation.
func TestKeyBoundsAreValidated(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	body := map[string]any{"project_id": h.projectID, "title": "bounded"}

	for _, tc := range []struct{ name, key, want string }{
		{"too long", strings.Repeat("k", maxIdempotencyKeyBytes+1), "255"},
		// Non-ASCII rather than a control byte: Go's own client refuses to
		// send a control character in a header value, so the byte a real
		// caller can actually get here is a high one.
		{"not ascii", "clé-café", "printable ASCII"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, out := h.postTaskKey(t, body, tc.key)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d %s, want 400", resp.StatusCode, out)
			}
			var env errorBody
			if err := json.Unmarshal(out, &env); err != nil {
				t.Fatalf("error body: %v (%s)", err, out)
			}
			if env.Error.Code != CodeValidationFailed {
				t.Errorf("code = %q, want %q", env.Error.Code, CodeValidationFailed)
			}
			if !strings.Contains(env.Error.Message, "Idempotency-Key") ||
				!strings.Contains(env.Error.Message, tc.want) {
				t.Errorf("message = %q, want it to name the field and %q", env.Error.Message, tc.want)
			}
		})
	}
	if n := h.rowCount(t, "tasks"); n != 0 {
		t.Errorf("tasks = %d, want none created behind a rejected key", n)
	}
}

// TestKeyCascadesWithItsTask: a key whose task has been destroyed has nothing
// left to replay, so the foreign key takes it away with the task (decision 6)
// and a later send of the same key creates a fresh task rather than 500ing on
// a dangling reference.
func TestKeyCascadesWithItsTask(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	body := map[string]any{"project_id": h.projectID, "title": "doomed"}
	first := h.createWithKey(t, body, "key-cascade")

	resp, out := h.doJSON(t, http.MethodDelete,
		fmt.Sprintf("/v1/projects/%d?force", h.projectID), nil)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("force delete project: %d %s", resp.StatusCode, out)
	}
	if n := h.rowCount(t, "tasks"); n != 0 {
		t.Fatalf("tasks = %d after a force delete, want 0", n)
	}
	if n := h.rowCount(t, "idempotency_keys"); n != 0 {
		t.Errorf("idempotency_keys = %d after task %d was destroyed, want 0", n, first.ID)
	}
}

// TestDoctorCountsIdempotencyKeys: the table is enumerated from the schema, so
// it appears in `database.table_rows` with no code of its own — which is what
// "expiry is exposed through doctor/storage metrics" asks for.
func TestDoctorCountsIdempotencyKeys(t *testing.T) {
	h := newDoctorHarness(t)
	if _, ok := h.report(t).Database.TableRows["idempotency_keys"]; !ok {
		t.Error("table_rows omits idempotency_keys")
	}
}
