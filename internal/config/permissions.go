package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DirPerm and FilePerm are the modes the config directory and config.yaml are
// created with (§12.2).
//
// config.yaml is the one file the daemon creates that can carry user-supplied
// secrets: values under environment.set are literal (§12.3), which is where an
// API token, a proxy credential or a license key ends up. It is owner-only for
// the same reason every other file the daemon owns already is — {data_dir}/token,
// transcripts, logs, tui.json. A stricter umask still applies; nothing here
// widens a mode.
const (
	DirPerm  os.FileMode = 0o700
	FilePerm os.FileMode = 0o600
)

// PermissionIssue is a config path whose mode grants access to group or other.
type PermissionIssue struct {
	Path string
	// Mode is the mode the path has, Want the mode §12.2 asks for.
	Mode os.FileMode
	Want os.FileMode
}

// Remediation is the exact command that fixes the issue, so a warning about a
// mode is one a reader can act on without looking anything up.
func (p PermissionIssue) Remediation() string {
	return fmt.Sprintf("chmod %04o %s", p.Want.Perm(), p.Path)
}

// CheckPermissions reports the config directory and config.yaml when either
// grants group or other access. It is read-only — `vincent doctor` warns from
// it, and the daemon logs from it before EnsureDefaultFile tightens what it
// found — and always empty on Windows, where the mode bits carry no access
// control. A path that does not exist yet is not an issue: the defaults apply
// until first start writes the file.
func CheckPermissions(dir string) ([]PermissionIssue, error) {
	var issues []PermissionIssue
	for _, want := range []PermissionIssue{
		{Path: dir, Want: DirPerm},
		{Path: filepath.Join(dir, FileName), Want: FilePerm},
	} {
		fi, err := os.Stat(want.Path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", want.Path, err)
		}
		perm := fi.Mode().Perm()
		if perm&sharedBits == 0 {
			continue
		}
		want.Mode = perm
		issues = append(issues, want)
	}
	return issues, nil
}

// tightenPermissions drops group and other access from the config directory
// and config.yaml. Owner bits are kept and contents are never touched: this
// runs on every daemon start over a file the user edits by hand, so the only
// thing it may change is who else can read it.
func tightenPermissions(dir string) error {
	issues, err := CheckPermissions(dir)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		if err := os.Chmod(issue.Path, issue.Mode&^sharedBits); err != nil {
			return fmt.Errorf("tighten permissions on %s: %w", issue.Path, err)
		}
	}
	return nil
}
