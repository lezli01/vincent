//go:build darwin

package service

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// plistTemplate is the launchd **user agent** (spec §12.1) — a LaunchAgent in
// the user's own ~/Library/LaunchAgents, not a root LaunchDaemon: vincent's
// trust boundary is the OS user (§16), and it reads that user's config.
//
// KeepAlive is conditional on a clean exit rather than unconditional. A
// daemon that exits 0 was asked to stop, and relaunching it would make
// `vincent daemon stop` impossible.
//
// PATH is in the environment because launchd's default one is
// /usr/bin:/bin:/usr/sbin:/sbin — no Homebrew, no npm prefix, no
// ~/.local/bin, so none of the agent CLIs resolve (T4.15).
const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>VINCENT_CONFIG_DIR</key><string>%s</string>
    <key>VINCENT_DATA_DIR</key><string>%s</string>
    <key>PATH</key><string>%s</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key><false/></dict>
</dict>
</plist>
`

// renderPlist fills the template. Split out so the plist's contents are
// testable without a launchd to install into.
//
// Every substituted value is XML-escaped. A home directory containing `&` is
// unusual; a PATH containing one is not, and launchd rejects a malformed
// plist with a message that says nothing about which character broke it.
func renderPlist(o Options) string {
	return fmt.Sprintf(plistTemplate, LaunchdName,
		xmlEscape(o.Exe), xmlEscape(o.Dirs.Config), xmlEscape(o.Dirs.Data), xmlEscape(o.Path))
}

func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		// strings.Builder never fails to write.
		return s
	}
	return b.String()
}

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchdName+".plist"), nil
}

// domain is the launchd GUI domain for this user, the target for bootstrap
// and bootout.
func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func install(ctx context.Context, o Options) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderPlist(o)), 0o644); err != nil { //nolint:gosec // a plist is world-readable by design
		return fmt.Errorf("write plist: %w", err)
	}
	// Replacing an existing registration: bootout first so bootstrap does not
	// fail with "service already loaded". Its failure is expected and ignored
	// when nothing was loaded.
	_, _ = launchctl(ctx, "bootout", domain()+"/"+LaunchdName)
	if _, err := launchctl(ctx, "bootstrap", domain(), path); err != nil {
		return err
	}
	// RunAtLoad covers boot; kickstart covers *now*, so install and start are
	// one step as they are on the other two platforms.
	if _, err := launchctl(ctx, "kickstart", domain()+"/"+LaunchdName); err != nil {
		return err
	}
	return nil
}

// LingerFailed has no macOS analog: a LaunchAgent with RunAtLoad survives
// logout and reboot on its own. It exists so callers stay platform-agnostic.
func LingerFailed(error) bool { return false }

func uninstall(ctx context.Context) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return ErrNotInstalled
	}
	_, _ = launchctl(ctx, "bootout", domain()+"/"+LaunchdName)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func query(ctx context.Context) (Status, error) {
	path, err := plistPath()
	if err != nil {
		return Status{}, err
	}
	st := Status{Name: LaunchdName, Unit: path}
	if _, err := os.Stat(path); err == nil {
		st.Installed = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return st, fmt.Errorf("stat plist: %w", err)
	}
	// `launchctl print` exits nonzero when the label is not loaded, which is
	// the answer rather than an error.
	out, _ := launchctl(ctx, "print", domain()+"/"+LaunchdName)
	st.Running = strings.Contains(out, "state = running")
	if st.Installed {
		st.Detail = "launchd user agent"
	}
	return st, nil
}
