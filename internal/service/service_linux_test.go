//go:build linux

package service

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
)

// TestRenderUnit checks the contents of the systemd unit without a systemd to
// install it into. The properties asserted are the ones a mistake would make
// silently wrong rather than loudly broken.
func TestRenderUnit(t *testing.T) {
	unit := renderUnit(Options{
		Exe:  "/opt/vincent/bin/vincent",
		Dirs: config.Dirs{Config: "/home/u/.config/vincent", Data: "/home/u/.local/share/vincent"},
	})

	for _, want := range []string{
		"ExecStart=/opt/vincent/bin/vincent daemon",
		"Environment=VINCENT_CONFIG_DIR=/home/u/.config/vincent",
		"Environment=VINCENT_DATA_DIR=/home/u/.local/share/vincent",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit is missing %q:\n%s", want, unit)
		}
	}

	// on-failure, never always: a daemon that exits 0 was asked to stop, and
	// Restart=always would make `vincent daemon stop` impossible.
	if !strings.Contains(unit, "Restart=on-failure") {
		t.Errorf("unit does not restart on failure only:\n%s", unit)
	}
	if strings.Contains(unit, "Restart=always") {
		t.Error("unit uses Restart=always; stopping the daemon would be impossible")
	}
	// A user unit: WantedBy=multi-user.target would be a system unit, which
	// needs root to manage a per-user daemon (§16 — the OS user is the trust
	// boundary).
	if strings.Contains(unit, "multi-user.target") {
		t.Error("unit targets multi-user.target; this is a user unit")
	}
}
