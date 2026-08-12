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

// Until T4.17 the Windows backend registered a machine-wide Windows Service
// under the same name. The SCM defaults an empty ServiceStartName to
// LocalSystem, so that daemon ran in the SYSTEM profile: a data dir under
// C:\Windows\System32\config\systemprofile that no TUI would ever probe, and
// §16's full-auto agents running with SYSTEM's privileges instead of the
// user's. This file exists to find that registration and take it away, so an
// upgrade does not end with two daemons where the invisible one is the
// privileged one.
//
// It is the only place left that talks to the SCM, and it can be deleted once
// no installation predating T4.17 plausibly remains.

// errLegacyService reports a pre-T4.17 registration that install must not
// stack a task on top of. Removing it needs the elevation registering it did.
var errLegacyService = errors.New(
	"a legacy vincent Windows Service is installed and runs as LocalSystem, which is why the TUI " +
		"keeps starting a daemon of its own\n\nremove it first, from an elevated (Administrator) prompt:\n" +
		"    vincent service uninstall\n\nthen run `vincent service install` again from an ordinary prompt")

// legacyServiceInstalled reports whether the old service registration is
// still there. It opens the SCM with query rights only, so asking never
// demands an elevated prompt — mgr.Connect requests SC_MANAGER_ALL_ACCESS and
// would report "access is denied" to an unelevated user with nothing to
// remove.
func legacyServiceInstalled(_ context.Context) bool {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false // an unreachable SCM means nothing to report
	}
	m := &mgr.Mgr{Handle: scm}
	defer func() { _ = m.Disconnect() }()

	name, err := windows.UTF16PtrFromString(Label)
	if err != nil {
		return false
	}
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false // absence is the answer, not a failure
	}
	_ = windows.CloseServiceHandle(h)
	return true
}

// removeLegacyService stops and deletes the old registration.
func removeLegacyService() error {
	m, err := mgr.Connect()
	if err != nil {
		return elevationHint(err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(Label)
	if err != nil {
		return nil // it went away between the check and here
	}
	defer func() { _ = s.Close() }()

	// A service deleted while still running is only marked for deletion and
	// lingers until its process exits, which makes the next install fail
	// confusingly.
	_ = stopAndWait(s)
	if err := s.Delete(); err != nil {
		return elevationHint(fmt.Errorf("delete legacy service: %w", err))
	}
	return nil
}

// stopAndWait asks a service to stop and gives the SCM a moment to reflect it.
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

// elevationHint turns the SCM's access-denied into the sentence that fixes it.
// Windows Services are machine-wide, so removing one needs the privileges that
// created it — the reason installing a *task* instead needs none.
func elevationHint(err error) error {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		strings.Contains(strings.ToLower(err.Error()), "access is denied") {
		return fmt.Errorf("%w\n\nthe legacy vincent registration is a Windows Service, which is "+
			"machine-wide: run this from an elevated (Administrator) prompt", err)
	}
	return err
}
