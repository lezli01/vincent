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
		Path: "/home/u/.npm-global/bin:/usr/local/bin:/usr/bin",
	})

	for _, want := range []string{
		"ExecStart=/opt/vincent/bin/vincent daemon",
		"Environment=VINCENT_CONFIG_DIR=/home/u/.config/vincent",
		"Environment=VINCENT_DATA_DIR=/home/u/.local/share/vincent",
		// Quoted, and carrying the install-time PATH: without it the user
		// manager's default applies and no agent CLI resolves (T4.15).
		`Environment="PATH=/home/u/.npm-global/bin:/usr/local/bin:/usr/bin"`,
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

// TestRenderUnitEscapes: `%` starts a specifier anywhere in a unit file, so an
// unescaped one in a path expands to something else — silently, and to a
// value that depends on which specifier letter followed it.
func TestRenderUnitEscapes(t *testing.T) {
	unit := renderUnit(Options{
		Exe:  "/opt/vincent/bin/vincent",
		Dirs: config.Dirs{Config: "/home/u/100%cfg", Data: "/d"},
		Path: `/home/u/my bin:/opt/50%n:/usr/bin`,
	})

	if !strings.Contains(unit, "Environment=VINCENT_CONFIG_DIR=/home/u/100%%cfg") {
		t.Errorf("percent in the config dir was not escaped:\n%s", unit)
	}
	// The space is why PATH is quoted: an unquoted Environment= splits on
	// whitespace and would drop everything after "my".
	if !strings.Contains(unit, `Environment="PATH=/home/u/my bin:/opt/50%%n:/usr/bin"`) {
		t.Errorf("PATH is not quoted-and-escaped:\n%s", unit)
	}
}
