package cli

import (
	"encoding/json"
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
