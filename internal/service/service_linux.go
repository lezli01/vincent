//go:build linux

package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// unitTemplate is the systemd **user** unit (spec §12.1). A user unit, not a
// system one: vincent's trust boundary is the OS user (§16), it reads that
// user's config and writes that user's data dir, and installing it
// system-wide would need root to manage a per-user daemon.
//
// Restart=on-failure rather than always: a daemon that exits 0 was asked to
// stop (`vincent daemon stop`, POST /v1/daemon/stop), and restarting it would
// make stopping impossible.
//
// PATH is set because a user unit inherits the user manager's default, which
// has no npm prefix, no nvm shim dir and no Homebrew — so the agent CLIs stop
// resolving the moment the daemon is managed rather than started by hand
// (T4.15).
const unitTemplate = `[Unit]
Description=vincent — local AI workload orchestrator
Documentation=https://github.com/lezli01/vincent
After=network.target

[Service]
Type=simple
ExecStart=%s daemon
Environment=VINCENT_CONFIG_DIR=%s
Environment=VINCENT_DATA_DIR=%s
Environment="PATH=%s"
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`

// renderUnit fills the template. Split out so the unit's contents are
// testable without a systemd to install into.
//
// Values are escaped for the unit-file syntax: `%` introduces a systemd
// specifier anywhere in a unit, so a directory or PATH entry containing one
// would expand to something else entirely. PATH is additionally quoted —
// unquoted `Environment=` splits on whitespace, which would silently truncate
// a PATH containing a space at that entry.
func renderUnit(o Options) string {
	return fmt.Sprintf(unitTemplate,
		unitEscape(o.Exe), unitEscape(o.Dirs.Config), unitEscape(o.Dirs.Data), unitQuoted(o.Path))
}

func unitEscape(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// unitQuoted escapes a value destined for the inside of a double-quoted
// unit-file string.
func unitQuoted(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")
	return r.Replace(s)
}

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", Label+".service"), nil
}

func install(ctx context.Context, o Options) error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	// G301: ~/.config/systemd/user takes the conventional mode; the unit file in
	// it is world-readable by design (see the WriteFile below).
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: see above
		return fmt.Errorf("create systemd user dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderUnit(o)), 0o644); err != nil { //nolint:gosec // a unit file is world-readable by design
		return fmt.Errorf("write unit: %w", err)
	}
	if _, err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if _, err := run(ctx, "systemctl", "--user", "enable", "--now", Label+".service"); err != nil {
		return err
	}
	// Without lingering, a user manager is torn down at logout and the
	// service does not survive a reboot — which is the entire promise being
	// made here. It usually needs a polkit prompt, so a failure is reported
	// as an instruction rather than swallowed or treated as fatal: the
	// service is installed and running either way.
	if _, err := run(ctx, "loginctl", "enable-linger"); err != nil {
		return fmt.Errorf("%w\n\nthe service is installed and running, but will stop at logout "+
			"until lingering is enabled:\n    sudo loginctl enable-linger $USER", errLingerFailed)
	}
	return nil
}

// errLingerFailed marks the one partial-success path, so a caller can print
// it as a warning rather than as a failed install.
var errLingerFailed = errors.New("could not enable lingering")

// LingerFailed reports whether err is the lingering warning — an install that
// worked but will not survive logout yet.
func LingerFailed(err error) bool { return errors.Is(err, errLingerFailed) }

func uninstall(ctx context.Context) error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return ErrNotInstalled
	}
	// Best effort in order: a unit that is already stopped must not block
	// removal of its file, which is the thing the user actually asked for.
	_, _ = run(ctx, "systemctl", "--user", "disable", "--now", Label+".service")
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove unit: %w", err)
	}
	_, _ = run(ctx, "systemctl", "--user", "daemon-reload")
	return nil
}

func query(ctx context.Context) (Status, error) {
	path, err := unitPath()
	if err != nil {
		return Status{}, err
	}
	st := Status{Name: Label + ".service", Unit: path}
	if _, err := os.Stat(path); err == nil {
		st.Installed = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return st, fmt.Errorf("stat unit: %w", err)
	}
	// `is-active` exits nonzero for an inactive unit, which is an answer
	// rather than an error.
	out, _ := run(ctx, "systemctl", "--user", "is-active", Label+".service")
	st.Running = strings.TrimSpace(out) == "active"
	if st.Installed {
		st.Detail = "systemd user unit"
	}
	return st, nil
}
