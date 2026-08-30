//go:build !windows

package api

import (
	"net/http"
	"os"
	"testing"

	"github.com/lezli01/vincent/internal/config"
)

// config.yaml can hold literal environment.set values and a notify.command
// carrying a secret (§12.2), which is why it is owner-only. A write that
// went through the daemon must not be the thing that widens it — including
// on a file that was already too permissive when the patch arrived.
func TestConfigPatchKeepsTheFileOwnerOnly(t *testing.T) {
	h := newConfigHarness(t)
	if err := os.Chmod(h.path, 0o644); err != nil {
		t.Fatalf("loosen: %v", err)
	}
	resp, body := h.patch(t, `{"log_level":"warn"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	info, err := os.Stat(h.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != config.FilePerm {
		t.Errorf("config.yaml mode = %o after a patch, want %o", got, config.FilePerm)
	}
}
