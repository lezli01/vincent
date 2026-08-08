package taskrun

import (
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"sync"

	"github.com/lezli01/vincent/internal/workflow"
)

// ReasonShellUnavailable is the failure reason when a step pins a shell that
// is not installed. A pinned shell is never silently replaced (phase 2
// decision) — a workflow that says pwsh must not quietly run under sh.
const ReasonShellUnavailable = "shell_unavailable"

// Shells resolves the shell used to run command and check steps (spec §8.3).
// The platform default is probed at first use and re-probed on config reload
// (phase 2 decision); a step's `shell:` pin is resolved per use and fails the
// step when the binary is missing.
type Shells struct {
	log *slog.Logger
	// probeFn resolves the platform default; tests substitute it.
	probeFn func() (Shell, error)

	mu     sync.Mutex
	probed bool
	def    Shell
	err    error
}

// Shell is a resolved shell: the binary plus the flags that make it run one
// command string.
type Shell struct {
	Name string
	Path string
	Args []string // flags preceding the command string
}

// NewShells returns a resolver logging its probe result once, and again
// whenever a re-probe changes it.
func NewShells(log *slog.Logger) *Shells { return &Shells{log: log, probeFn: probeDefault} }

// Default returns the platform default shell: /bin/sh on POSIX, pwsh on
// Windows falling back to powershell (spec §8.3). The probe runs at first
// use and its result is reused until Reprobe.
func (s *Shells) Default() (Shell, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.probed {
		s.probe()
	}
	return s.def, s.err
}

// Reprobe re-resolves the platform default — a config reload may follow a
// shell being installed or removed (§8.3). Logs only when the resolution
// changed.
func (s *Shells) Reprobe() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.probed {
		return // first use will probe and log
	}
	s.probe()
}

// probe runs with mu held.
func (s *Shells) probe() {
	prev, prevErr, hadProbed := s.def, s.err, s.probed
	s.def, s.err = s.probeFn()
	s.probed = true
	switch {
	case s.err != nil:
		if !hadProbed || prevErr == nil {
			s.log.Warn("no default shell found; command steps will fail", "error", s.err)
		}
	case !hadProbed || prevErr != nil || prev.Name != s.def.Name || prev.Path != s.def.Path:
		s.log.Info("shell detected", "shell", s.def.Name, "path", s.def.Path)
	}
}

// For returns the shell a step should use: its `shell:` pin when set, else
// the platform default.
func (s *Shells) For(pin string) (Shell, error) {
	if pin == "" {
		return s.Default()
	}
	return lookupShell(pin)
}

func probeDefault() (Shell, error) {
	if runtime.GOOS == "windows" {
		if sh, err := lookupShell(workflow.ShellPwsh); err == nil {
			return sh, nil
		}
		// Windows PowerShell 5.1 ships with the OS; pwsh may not be installed.
		if path, err := exec.LookPath("powershell"); err == nil {
			return Shell{Name: "powershell", Path: path, Args: []string{"-NoProfile", "-Command"}}, nil
		}
		return Shell{}, fmt.Errorf("neither pwsh nor powershell found on PATH")
	}
	return lookupShell(workflow.ShellSh)
}

// lookupShell resolves one of the §8.3 shell names to its binary.
func lookupShell(name string) (Shell, error) {
	var candidate, flag string
	var extra []string
	switch name {
	case workflow.ShellSh:
		candidate, flag = "/bin/sh", "-c"
		if runtime.GOOS == "windows" {
			candidate = "sh" // Git Bash's sh, if the author put it on PATH
		}
	case workflow.ShellPwsh:
		candidate = "pwsh"
		extra, flag = []string{"-NoProfile"}, "-Command"
	case workflow.ShellCmd:
		candidate, flag = "cmd", "/C"
	default:
		return Shell{}, fmt.Errorf("unknown shell %q", name)
	}
	path, err := exec.LookPath(candidate)
	if err != nil {
		return Shell{}, fmt.Errorf("shell %q not found: %w", name, err)
	}
	return Shell{Name: name, Path: path, Args: append(extra, flag)}, nil
}

// command builds the argv running one rendered command string.
func (sh Shell) command(script string) []string {
	return append(append([]string{sh.Path}, sh.Args...), script)
}
