package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestProjectLsStats is `vincent project ls --stats` (task 132.1) against the
// real handlers, so apiclient.ProjectStats cannot drift from the server's
// wire type. The harness's project holds one running task.
func TestProjectLsStats(t *testing.T) {
	newLiveHarness(t)

	out, errOut, code := runCLI(t, "project", "ls")
	if code != 0 {
		t.Fatalf("project ls: exit %d (%s)", code, errOut)
	}
	if strings.Contains(out, "ACTIVE") {
		t.Errorf("project ls without --stats grew stats columns:\n%s", out)
	}
	out, errOut, code = runCLI(t, "project", "ls", "--json")
	if code != 0 {
		t.Fatalf("project ls --json: exit %d (%s)", code, errOut)
	}
	if strings.Contains(out, `"stats"`) {
		t.Errorf("project ls --json without --stats carries stats: %s", out)
	}

	out, errOut, code = runCLI(t, "project", "ls", "--stats")
	if code != 0 {
		t.Fatalf("project ls --stats: exit %d (%s)", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("project ls --stats = %q, want a header and one row", out)
	}
	header, row := strings.Fields(lines[0]), strings.Fields(lines[1])
	if got := strings.Join(header[len(header)-4:], " "); got != "ACTIVE ATTN ISSUES CHATS" {
		t.Errorf("stats columns = %q", got)
	}
	if got := strings.Join(row[len(row)-4:], " "); got != "1 0 0 0" {
		t.Errorf("stats cells = %q, want 1 0 0 0 (one running task)", got)
	}

	out, errOut, code = runCLI(t, "project", "ls", "--stats", "--json")
	if code != 0 {
		t.Fatalf("project ls --stats --json: exit %d (%s)", code, errOut)
	}
	var list []apiclient.Project
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 {
		t.Fatalf("decode %q: %v", out, err)
	}
	st := list[0].Stats
	if st == nil {
		t.Fatalf("--stats --json row has no stats: %s", out)
	}
	if st.Tasks.Active != 1 || st.Tasks.ByState["running"] != 1 || st.LastActivityAt == nil {
		t.Errorf("stats = %+v, want one running active task and a last activity", st)
	}
}

// TestProjectLsStatsDegraded is `project ls --stats` when the daemon could not
// count and served `"stats": null` (review F3): the table shows a dash in
// every stats column, and --json keeps the key as null rather than dropping
// it, so a script can still tell "asked but failed" from "not asked". The
// real handler's answer is rewritten on the way out because the harness's
// store has no seam to fail the count; the server side of the degrade is
// TestProjectStatsDegradeToNull.
func TestProjectLsStatsDegraded(t *testing.T) {
	newLiveHarness(t, withHandlerWrap(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/projects" || r.URL.Query().Get("stats") != "true" {
				next.ServeHTTP(w, r)
				return
			}
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, r)
			var rows []map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
				t.Errorf("decode the real answer %q: %v", rec.Body.String(), err)
			}
			for _, row := range rows {
				row["stats"] = nil
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(rows)
		})
	}))

	out, errOut, code := runCLI(t, "project", "ls", "--stats")
	if code != 0 {
		t.Fatalf("project ls --stats: exit %d (%s)", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("project ls --stats = %q, want a header and one row", out)
	}
	row := strings.Fields(lines[1])
	if got := strings.Join(row[len(row)-4:], " "); got != "- - - -" {
		t.Errorf("stats cells = %q, want a dash in each", got)
	}

	out, errOut, code = runCLI(t, "project", "ls", "--stats", "--json")
	if code != 0 {
		t.Fatalf("project ls --stats --json: exit %d (%s)", code, errOut)
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 {
		t.Fatalf("decode %q: %v", out, err)
	}
	st, ok := list[0]["stats"]
	if !ok || string(st) != "null" {
		t.Errorf(`--stats --json row stats = %q (present %v), want "stats": null`, string(st), ok)
	}
}
