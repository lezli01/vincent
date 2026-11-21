package store_test

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestInstallationID: a fresh database carries one id, 16 lowercase hex
// characters, read back the same every time; two fresh databases differ.
func TestInstallationID(t *testing.T) {
	open := func() *store.Store {
		st, err := store.Open(filepath.Join(t.TempDir(), "v.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
	a, b := open(), open()
	idA, err := a.InstallationID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(idA) {
		t.Errorf("id = %q, want 16 lowercase hex characters", idA)
	}
	if again, _ := a.InstallationID(t.Context()); again != idA {
		t.Errorf("id changed: %q then %q", idA, again)
	}
	if idB, _ := b.InstallationID(t.Context()); idB == idA {
		t.Errorf("two databases share id %q", idA)
	}
}
