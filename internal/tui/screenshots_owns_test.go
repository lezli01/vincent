package tui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// ownsHaystack is one branch of scripts/screenshots.sh's owns(): the kind and
// the jq array it joins into the string a needle must be a substring of.
var ownsHaystack = regexp.MustCompile(`(?s)\n    (task|chat|issue)\)\n[^\n]*rows=[^\n]*\n      hay='(.*?)'\n`)

// TestScreenshotOwnsMatchesTUIFilters holds owns() in scripts/screenshots.sh
// to the `/` filters it stands in for (review F3 on #742). owns() exists so a
// tape never photographs a filter that matches no row, or several; matched on
// titles alone, a needle that also hit a label, a branch or a state word
// passed it with the board showing more than one row. Each haystack is run
// through jq against the same fixtures the Go filter sees, needle by needle.
func TestScreenshotOwnsMatchesTUIFilters(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is not on PATH")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "screenshots.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(raw), "\r\n", "\n")
	start := strings.Index(script, "\nowns() {")
	if start < 0 {
		t.Fatal("no owns() in scripts/screenshots.sh; the pattern is stale")
	}
	end := strings.Index(script[start:], "\n}\n")
	hays := map[string]string{}
	for _, m := range ownsHaystack.FindAllStringSubmatch(script[start:start+end], -1) {
		hays[m[1]] = m[2]
	}
	if len(hays) != 3 {
		t.Fatalf("found haystacks for %d kinds in owns(), want task, chat and issue; the pattern is stale", len(hays))
	}

	count := func(t *testing.T, hay string, rows any, needle string) int {
		t.Helper()
		in, err := json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		// The filter expression owns() builds, with $hay spliced in.
		expr := "[.[] | select(" + hay + ` | map(. // "") | join(" ") | ascii_downcase | contains($n | ascii_downcase))] | length`
		cmd := exec.Command(jq, "--arg", "n", needle, expr)
		cmd.Stdin = strings.NewReader(string(in))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("jq %s: %v", expr, err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatalf("jq printed %q", out)
		}
		return n
	}

	t.Run("task", func(t *testing.T) {
		tasks := []apiclient.Task{
			{ID: 12, Title: "Harden the session store", State: stateAwaitingGate},
			{ID: 3, Title: "Add design tokens", State: stateAwaitingInput},
			{ID: 41, Title: "Split the v2 storage", State: stateAwaitingChildren},
			{ID: 5, Title: "Read replica", State: "running"},
		}
		for _, q := range []string{"harden", "12", "1", "approval", "awaiting", "input", "lanes", "running", "gate", "nothing"} {
			if got, want := count(t, hays["task"], tasks, q), len(filterTasks(tasks, q)); got != want {
				t.Errorf("needle %q: owns() counts %d tasks, the board's filter shows %d", q, got, want)
			}
		}
	})
	t.Run("chat", func(t *testing.T) {
		chats := []apiclient.Chat{
			{ID: 1, Title: "Rate limit design", Agent: "claude", Branch: "vincent/chat-1-rate-limit"},
			{ID: 2, Title: "Ask about retries", Agent: "codex", Branch: "feature/retries"},
			{ID: 3, Title: "No branch yet", Agent: "cursor"},
		}
		for _, q := range []string{"rate limit", "claude", "codex", "retries", "vincent/", "chat", "nothing"} {
			if got, want := count(t, hays["chat"], chats, q), len(filterChats(chats, q)); got != want {
				t.Errorf("needle %q: owns() counts %d chats, the chats board's filter shows %d", q, got, want)
			}
		}
	})
	t.Run("issue", func(t *testing.T) {
		issues := []apiclient.Issue{
			{ID: 7, Title: "Login loops", Kind: "bug", Labels: []string{"auth", "p1"}},
			{ID: 70, Title: "Dark mode", Kind: "feature"},
			{ID: 8, Title: "Auth docs", Kind: "task", Labels: []string{"docs"}},
		}
		for _, q := range []string{"#7", "7", "auth", "bug", "feature", "docs", "p1", "nothing"} {
			want := 0
			for _, iss := range issues {
				if issueMatches(iss, strings.ToLower(q)) {
					want++
				}
			}
			if got := count(t, hays["issue"], issues, q); got != want {
				t.Errorf("needle %q: owns() counts %d issues, the issues filter shows %d", q, got, want)
			}
		}
	})
}
