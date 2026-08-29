package cli

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/release"
	"github.com/lezli01/vincent/internal/selfupdate"
	"github.com/lezli01/vincent/internal/version"
)

// `vincent update --check`'s documented exit codes, driven against an
// httptest release feed.
//
// This runs runUpdate in-process rather than exec'ing the built binary, which
// every other exit-code suite in this package does. The reason is deliberate:
// the release source is an unexported field precisely so it cannot be set from
// outside, because it names a URL this command downloads a binary from and
// executes. An env var or hidden flag that redirects it would be a real
// hazard, and it would exist only to let a test spawn a subprocess. The exit
// codes are the contract, and asserting them on the function that returns them
// asserts the contract.
func TestUpdateCheckExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		handler  http.HandlerFunc
		wantCode int
		wantOut  string
	}{
		{
			// Exit 0: up to date. The test binary reports "dev", which is
			// never behind — the rule the check is built on.
			name: "up to date",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"tag_name":"v0.0.1","published_at":"2026-08-21T09:31:07Z"}`))
			},
			wantCode: 0,
			wantOut:  "up to date",
		},
		{
			// Exit 1: the check failed. It must not collapse into "up to
			// date" — a script cannot tell the two apart any other way.
			name: "rate limited",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			},
			wantCode: 1,
		},
		{
			// Exit 2: an update is available. Only reachable from a release
			// build, since `dev` is never behind — so this case is asserted
			// against the same rule rather than skipped.
			name: "update available",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"tag_name":"v99.0.0","published_at":"2026-08-21T09:31:07Z"}`))
			},
			wantCode: exitCodeForNewRelease(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			t.Cleanup(srv.Close)

			cmd, out := updateTestCmd(t)
			err := runUpdate(cmd, updateOpts{checkOnly: true, baseURL: srv.URL})
			if got := exitCodeOf(err); got != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (err %v, output %q)",
					got, tc.wantCode, err, out.String())
			}
			if tc.wantOut != "" && !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("output %q does not mention %q", out.String(), tc.wantOut)
			}
		})
	}
}

// A prerelease is never reported as an available update, all the way out to
// the exit code: the feed offering one is the same answer as no release at
// all, so the check fails rather than claiming an update.
func TestUpdateCheckIgnoresPrerelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v99.0.0-rc.1","prerelease":true,
			"published_at":"2026-08-27T18:02:11Z"}`))
	}))
	t.Cleanup(srv.Close)

	cmd, out := updateTestCmd(t)
	err := runUpdate(cmd, updateOpts{checkOnly: true, baseURL: srv.URL})
	if exitCodeOf(err) == 2 {
		t.Fatalf("a prerelease was reported as an available update: %s", out.String())
	}
}

// --dry-run downloads nothing and swaps nothing. The feed answers, and the
// download server fails the test if it is ever reached.
func TestUpdateDryRunDownloadsNothing(t *testing.T) {
	if release := version.Version(); release == "dev" {
		// `dev` is never behind, so there is no update to decline. The rule
		// is asserted in internal/release; this test is about the flag.
		t.Skip("a dev build is never behind, so --dry-run has nothing to decline")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v99.0.0","published_at":"2026-08-21T09:31:07Z"}`))
	}))
	t.Cleanup(srv.Close)
	downloads := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("--dry-run downloaded a release asset")
	}))
	t.Cleanup(downloads.Close)

	cmd, out := updateTestCmd(t)
	err := runUpdate(cmd, updateOpts{
		dryRun: true, baseURL: srv.URL, downloadBase: downloads.URL,
		executable: t.TempDir() + "/vincent",
	})
	if err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "--dry-run") {
		t.Errorf("output %q does not say nothing was changed", out.String())
	}
}

// A package-managed install prints that channel's command rather than
// touching the binary. The channel table is tested in internal/selfupdate;
// what this asserts is that the command reaches the user.
func TestUpdatePrintsPackageManagerCommand(t *testing.T) {
	for _, c := range []selfupdate.Channel{
		selfupdate.ChannelHomebrew, selfupdate.ChannelScoop,
		selfupdate.ChannelWinGet, selfupdate.ChannelMise,
	} {
		cmd, out := updateTestCmd(t)
		err := updateNotOwned(cmd, out, "v0.1.0",
			releaseFixture(), c)
		if got := exitCodeOf(err); got != 2 {
			t.Errorf("%s: exit code = %d, want 2", c, got)
		}
		if !strings.Contains(out.String(), c.UpgradeCommand()) {
			t.Errorf("%s: output %q does not carry %q", c, out.String(), c.UpgradeCommand())
		}
	}
}

// An install nothing recognises is treated as package-managed: it prints
// where to get the release and exits 2, and does not offer to swap.
func TestUpdateUnknownChannelChangesNothing(t *testing.T) {
	cmd, out := updateTestCmd(t)
	err := updateNotOwned(cmd, out, "v0.1.0", releaseFixture(), selfupdate.ChannelUnknown)
	if got := exitCodeOf(err); got != 2 {
		t.Errorf("exit code = %d, want 2", got)
	}
	if !strings.Contains(out.String(), "could not tell how this binary was installed") {
		t.Errorf("output %q does not say why it declined", out.String())
	}
}

func releaseFixture() release.Release {
	return release.Release{
		Version: "v99.0.0",
		URL:     "https://github.com/lezli01/vincent/releases/tag/v99.0.0",
	}
}

// updateTestCmd returns the command and its stdout. Stderr goes to a buffer
// too — a command writing to the test process's stderr would be noise — but no
// assertion reads it, so it is not returned.
func updateTestCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	cmd := newUpdateCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(t.Context())
	return cmd, out
}

// exitCodeOf reads the code a command returned; nil is 0.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return -1
}

// exitCodeForNewRelease is 2 in a released build and 0 in a `dev` one,
// because a build from source is never behind. Spelling it out here keeps the
// table honest on both.
func exitCodeForNewRelease() int {
	if version.Version() == "dev" {
		return 0
	}
	return 2
}
