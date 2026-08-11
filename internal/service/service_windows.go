//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Windows has no per-user service: the SCM is machine-wide, so install and
// uninstall need elevation. That is the one place the three platforms
// genuinely differ in what they ask of the user, and it is reported plainly
// rather than as an opaque "access is denied".
//
// The service is registered to run as the invoking user via its own account
// is *not* attempted: a service configured with LocalSystem would read a
// different profile than the CLI, so the account is left at the SCM default
// and the daemon's own directories are pinned through the environment
// captured at install time (Options.resolve).

func install(ctx context.Context, o Options) error {
	m, err := mgr.Connect()
	if err != nil {
		return elevationHint(err)
	}
	defer func() { _ = m.Disconnect() }()

	// Replace an existing registration rather than failing, matching the
	// idempotence of `daemon start` and of the other two backends.
	if s, err := m.OpenService(Label); err == nil {
		_ = stopAndWait(s)
		err = s.Delete()
		_ = s.Close()
		if err != nil {
			return fmt.Errorf("delete existing service: %w", err)
		}
	}

	s, err := m.CreateService(Label, o.Exe, mgr.Config{
		DisplayName:  "vincent — local AI workload orchestrator",
		Description:  "Runs AI coding-agent workloads in the background (https://github.com/lezli01/vincent).",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}, "daemon")
	if err != nil {
		return elevationHint(err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	_ = ctx // the SCM calls are synchronous; ctx is accepted for symmetry
	return nil
}

// LingerFailed has no Windows analog: an automatic-start service survives
// logout and reboot by definition.
func LingerFailed(error) bool { return false }

func uninstall(ctx context.Context) error {
	// Check for existence with a read-only handle first. mgr.Connect asks for
	// SC_MANAGER_ALL_ACCESS, which needs elevation — so connecting to find
	// out whether anything is installed would report "access is denied" to an
	// unelevated user who has nothing to uninstall. Absence is answerable
	// without privileges, and answering it is friendlier than demanding them.
	if st, err := query(ctx); err == nil && !st.Installed {
		return ErrNotInstalled
	}

	m, err := mgr.Connect()
	if err != nil {
		return elevationHint(err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(Label)
	if err != nil {
		return ErrNotInstalled
	}
	defer func() { _ = s.Close() }()

	_ = stopAndWait(s)
	if err := s.Delete(); err != nil {
		return elevationHint(fmt.Errorf("delete service: %w", err))
	}
	_ = ctx
	return nil
}

// query opens the SCM and the service with query rights only, so reporting
// status never requires an elevated prompt. mgr.Connect and mgr.Mgr.OpenService
// both request full access, which would.
func query(_ context.Context) (Status, error) {
	st := Status{Name: Label}
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return st, nil //nolint:nilerr // an unreachable SCM means nothing to report
	}
	m := &mgr.Mgr{Handle: scm}
	defer func() { _ = m.Disconnect() }()

	name, err := windows.UTF16PtrFromString(Label)
	if err != nil {
		return st, fmt.Errorf("service name: %w", err)
	}
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return st, nil //nolint:nilerr // absence is the answer, not a failure
	}
	s := &mgr.Service{Name: Label, Handle: h}
	defer func() { _ = s.Close() }()

	st.Installed = true
	st.Detail = "Windows Service"
	status, err := s.Query()
	if err == nil {
		st.Running = status.State == svc.Running
	}
	return st, nil
}

// stopAndWait asks a service to stop and gives the SCM a moment to reflect
// it; a service being deleted while still running is marked for deletion and
// lingers until its process exits, which makes a reinstall fail confusingly.
func stopAndWait(s *mgr.Service) error {
	status, err := s.Control(svc.Stop)
	if err != nil {
		return err
	}
	for range 100 {
		if status.State == svc.Stopped {
			return nil
		}
		windows.SleepEx(100, false)
		if status, err = s.Query(); err != nil {
			return err
		}
	}
	return errors.New("service did not stop in time")
}

// elevationHint turns the SCM's access-denied into the sentence that fixes
// it. Windows Services are machine-wide, so this is not a bug to work around
// but a fact to state.
func elevationHint(err error) error {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		strings.Contains(strings.ToLower(err.Error()), "access is denied") {
		return fmt.Errorf("%w\n\nWindows services are machine-wide: run this from an "+
			"elevated (Administrator) prompt", err)
	}
	return err
}
