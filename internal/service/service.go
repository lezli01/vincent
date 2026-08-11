// Package service installs the daemon as an OS-managed background service so
// it survives logout and reboot (spec §12.1): a Windows Service, a launchd
// user agent, or a systemd user unit.
//
// Each platform is hand-rolled in a build-tagged file rather than delegated
// to a service library. That keeps the one platform seam this repo has always
// kept explicit — the same bias that chose the git CLI over go-git and a
// hand-rolled data-dir resolver over an xdg package — and each backend is
// about fifteen lines of template plus a subprocess call (W decision).
package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/lezli01/vincent/internal/config"
)

// Label is the service's OS-level identity: the Windows service name, the
// launchd label, and the systemd unit name. Reverse-DNS on macOS convention,
// plain elsewhere.
const (
	Label       = "vincent"
	LaunchdName = "dev.lezli01.vincent"
)

// ErrUnsupported is returned on a platform with no backend.
var ErrUnsupported = errors.New("service management is not supported on this platform")

// ErrNotInstalled is returned by Uninstall when there is nothing to remove.
var ErrNotInstalled = errors.New("no vincent service is installed")

// Status describes the installed service.
type Status struct {
	Installed bool
	Running   bool
	// Name is the OS-level identifier a user would type into their own
	// tooling (`sc query vincent`, `systemctl --user status vincent`).
	Name string
	// Unit is the on-disk unit/plist path; empty on Windows, where the
	// registration lives in the SCM rather than a file.
	Unit string
	// Detail is platform-native extra context, shown verbatim.
	Detail string
}

// Options configure an install.
type Options struct {
	// Exe is the binary the service will run. Empty resolves the running
	// executable, which is what makes `vincent service install` mean "install
	// this binary" rather than "install whatever is on PATH later".
	Exe string
	// Dirs are the config and data directories baked into the unit.
	Dirs config.Dirs
	// Path is the PATH the service runs with. Empty captures the PATH in
	// effect at install time — see resolve. Ignored on Windows, where the SCM
	// has no per-service environment and a service inherits the machine
	// environment instead (T4.15).
	Path string
}

// resolve fills in defaults and returns an absolute executable path.
//
// The dirs are captured at install time on purpose. A service does not
// inherit the shell that installed it, so VINCENT_CONFIG_DIR/VINCENT_DATA_DIR
// set in a terminal would silently not apply and the service would quietly
// use a different database than the CLI. Writing the resolved paths into the
// unit makes "install now" mean "run with the directories I am using now".
//
// PATH is captured for the same reason, found the same way (T4.15, macOS
// service leg): a launchd agent runs with launchd's own minimal PATH and a
// systemd user unit with systemd's, neither of which contains Homebrew, npm
// globals, or `~/.local/bin` — so every agent CLI resolved by exec.LookPath
// went missing the moment the daemon was managed rather than started by hand,
// and the TUI reported no adapters at all. The shell that runs `service
// install` has the PATH that works; baking it in is what "install this
// binary, as it runs now" already meant for the dirs.
func (o Options) resolve() (Options, error) {
	if o.Exe == "" {
		exe, err := os.Executable()
		if err != nil {
			return o, fmt.Errorf("resolve own executable: %w", err)
		}
		o.Exe = exe
	}
	if o.Dirs.Config == "" || o.Dirs.Data == "" {
		dirs, err := config.ResolveDirs()
		if err != nil {
			return o, err
		}
		if o.Dirs.Config == "" {
			o.Dirs.Config = dirs.Config
		}
		if o.Dirs.Data == "" {
			o.Dirs.Data = dirs.Data
		}
	}
	if o.Path == "" {
		o.Path = os.Getenv("PATH")
	}
	return o, nil
}

// Install registers the daemon with the platform service manager and starts
// it. It is idempotent in the way `daemon start` is: installing over an
// existing installation replaces it rather than failing.
func Install(ctx context.Context, opts Options) error {
	o, err := opts.resolve()
	if err != nil {
		return err
	}
	return install(ctx, o)
}

// Uninstall stops and deregisters the service. It returns ErrNotInstalled
// when there is nothing there, so a caller can report that plainly instead of
// treating it as a failure.
func Uninstall(ctx context.Context) error { return uninstall(ctx) }

// Query reports what is installed.
func Query(ctx context.Context) (Status, error) { return query(ctx) }
