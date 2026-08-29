package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
)

// Options configure an Updater. Every zero value is the production default,
// so `New(Options{})` is the one `vincent update` builds.
type Options struct {
	// DownloadBase is the release-asset root; empty means DownloadBase.
	DownloadBase string
	// HTTP is the transport; nil means a client bounded by DownloadTimeout.
	HTTP *http.Client
	// CosignPath overrides the cosign binary; empty resolves it from PATH.
	// Tests point it at a stub whose exit code they choose, the way
	// cmd/fakeagent stands in for an agent CLI.
	CosignPath string
	// RequireSignature makes a missing cosign fatal. Default false: the
	// checksum check runs alone and the caller is told plainly that the
	// signature was not verified (decision 1).
	RequireSignature bool
	// Executable is the file to replace; empty means the running binary.
	Executable string
}

// Updater downloads, verifies and installs a release.
type Updater struct {
	downloadBase     string
	http             *http.Client
	cosignPath       string
	requireSignature bool
	executable       string
}

// New builds an Updater. It performs no I/O.
func New(opts Options) *Updater {
	base := opts.DownloadBase
	if base == "" {
		base = DownloadBase
	}
	h := opts.HTTP
	if h == nil {
		h = &http.Client{Timeout: DownloadTimeout}
	}
	return &Updater{
		downloadBase:     base,
		http:             h,
		cosignPath:       opts.CosignPath,
		requireSignature: opts.RequireSignature,
		executable:       opts.Executable,
	}
}

// Result reports what an Apply did, in the terms `vincent update` prints and
// `--json` serializes.
type Result struct {
	// Version is the release that was installed.
	Version string `json:"version"`
	// Path is the file that was replaced.
	Path string `json:"path"`
	// SignatureVerified is false when no cosign was on PATH and the caller
	// did not require one. It is reported rather than assumed: a user who
	// gets a successful update and a false here has been told exactly what
	// was and was not checked.
	SignatureVerified bool `json:"signature_verified"`
}

// ErrNotOwned is returned when the binary belongs to a package manager. The
// caller prints that channel's command and exits 2; nothing is downloaded.
var ErrNotOwned = errors.New("this vincent was installed by a package manager")

// Apply installs the release tagged version over the running binary.
//
// The order is the guarantee, and it is the release footer's order: verify
// checksums.txt's signature, verify the archive against that file, extract,
// and only then touch the filesystem. Every failure before the swap leaves the
// old binary untouched because nothing has been written to it yet; a failure
// during the swap rolls back (see Swap).
func (u *Updater) Apply(ctx context.Context, version string) (Result, error) {
	dest := u.executable
	if dest == "" {
		ch, exe := Detect()
		if !ch.Owned() {
			return Result{}, ErrNotOwned
		}
		dest = exe
	}
	if dest == "" {
		return Result{}, errors.New("cannot locate the running vincent binary")
	}

	tag := version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	asset := hostAsset(version)

	checksums, err := u.fetch(ctx, tag, "checksums.txt")
	if err != nil {
		return Result{}, err
	}
	signed, err := u.checkSignature(ctx, tag, checksums)
	if err != nil {
		return Result{}, err
	}
	archive, err := u.fetch(ctx, tag, asset)
	if err != nil {
		return Result{}, err
	}
	if err := verifyChecksum(checksums, asset, archive); err != nil {
		return Result{}, err
	}
	binary, err := extractBinary(archive, runtime.GOOS)
	if err != nil {
		return Result{}, err
	}
	if err := Swap(dest, binary); err != nil {
		return Result{}, err
	}
	return Result{Version: tag, Path: dest, SignatureVerified: signed}, nil
}

// checkSignature runs the cosign leg and reports whether it actually ran.
//
// A missing cosign is the one failure that does not refuse: it degrades to
// the checksum alone, and the caller says so. Anything cosign itself objects
// to — a bad signature, a certificate that does not match the project's
// identity — refuses the swap. `--require-signature` collapses the first case
// into the second for a caller who wants the guarantee rather than the
// convenience.
func (u *Updater) checkSignature(ctx context.Context, tag string, checksums []byte) (bool, error) {
	sig, err := u.fetch(ctx, tag, "checksums.txt.sig")
	if err != nil {
		if u.requireSignature {
			return false, err
		}
		// A release with no signature asset at all is old or hand-made.
		// Without --require-signature that is the same degraded case as a
		// missing cosign, and it is reported the same way.
		return false, nil
	}
	cert, err := u.fetch(ctx, tag, "checksums.txt.pem")
	if err != nil {
		if u.requireSignature {
			return false, err
		}
		return false, nil
	}
	if err := u.verifySignature(ctx, checksums, sig, cert); err != nil {
		if errors.Is(err, ErrCosignMissing) && !u.requireSignature {
			return false, nil
		}
		if errors.Is(err, ErrCosignMissing) {
			return false, fmt.Errorf(
				"%w: install it, or drop --require-signature to verify the checksum alone", err)
		}
		return false, err
	}
	return true, nil
}
