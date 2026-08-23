package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadDirRejectsOversizedFile is the portable half of issue #136: loadDir
// reads every matching name with os.ReadFile, which allocates the whole file
// whatever its size, so a huge regular file dropped into a registered repo's
// .vincent/workflows is slurped into the daemon while the scope is catalogued.
// The file below is valid YAML, so a rejection can only come from a size bound
// and never from the parser.
func TestLoadDirRejectsOversizedFile(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	writeWorkflow(t, globalDir, "good", manualWorkflow("good", "regular"))

	// 32 MiB: far above any bound this could plausibly be fixed with, so the
	// test does not encode a particular limit.
	const oversized = 32 << 20
	src := manualWorkflow("huge", "oversized") + "# " + strings.Repeat("x", oversized)
	big := filepath.Join(globalDir, "huge.yaml")
	if err := os.WriteFile(big, []byte(src), 0o600); err != nil {
		t.Fatalf("write %s: %v", big, err)
	}

	reg.ReloadGlobal()

	e, ok := reg.Lookup(0, "huge")
	if ok && e.Valid() {
		t.Errorf("Lookup(\"huge\") returned a valid entry from a %d-byte file; a workflow source must be bounded", len(src))
	}
	if e.Source == src {
		t.Errorf("entry captured all %d bytes of the file as its source", len(e.Source))
	}
	// The rejected sibling must not cost the valid file its entry.
	if e, ok := reg.Lookup(0, "good"); !ok || !e.Valid() {
		t.Errorf(`Lookup("good") = %+v, %v; the regular file must stay available`, e, ok)
	}
}
