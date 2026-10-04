package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestTaskRecordCountsAgree holds docs/tasks/ to its own convention
// (docs/tasks/README.md): a task's done/total count is written twice, on the
// document's **Status:** line and on its index row, and both say what the
// checkboxes say. Task 130's header sat two items behind its checkboxes and
// its index row while both of those moved (review F3).
//
// Only what can be read is held: a header with no (n/m), or a document whose
// items are not `- [x] **NNN.n**` list entries, is skipped rather than guessed.
func TestTaskRecordCountsAgree(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "tasks")
	index, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("read the index: %v", err)
	}
	row := regexp.MustCompile(`(?m)^\| \[\d+\]\(([^)]+\.md)\) \|.*\((\d+)/(\d+)\) \|\s*$`)
	status := regexp.MustCompile(`(?m)^\*\*Status:\*\*.*?\((\d+)/(\d+)\)`)
	item := regexp.MustCompile(`(?m)^- \[(.)\] \*\*(\d+\.\d+)\*\*`)

	rows := row.FindAllSubmatch(index, -1)
	if len(rows) == 0 {
		t.Fatal("no index row carries a (n/m) count; the row pattern no longer matches the index")
	}
	for _, r := range rows {
		name, idxDone, idxTotal := string(r[1]), string(r[2]), string(r[3])
		doc, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("index row links %s: %v", name, err)
			continue
		}
		head := status.FindSubmatch(doc)
		if head == nil {
			continue
		}
		if got, want := string(head[1])+"/"+string(head[2]), idxDone+"/"+idxTotal; got != want {
			t.Errorf("%s: **Status:** says (%s), its index row says (%s)", name, got, want)
		}
		done, total := map[string]bool{}, map[string]bool{}
		for _, m := range item.FindAllSubmatch(doc, -1) {
			total[string(m[2])] = true
			if string(m[1]) == "x" {
				done[string(m[2])] = true
			}
		}
		if len(total) == 0 {
			continue
		}
		if got := strconv.Itoa(len(done)) + "/" + strconv.Itoa(len(total)); got != string(head[1])+"/"+string(head[2]) {
			t.Errorf("%s: **Status:** says (%s/%s), its checkboxes say (%s)", name, head[1], head[2], got)
		}
	}
}
