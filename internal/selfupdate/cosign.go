package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// The signature leg (task 055 decision 1).
//
// `.goreleaser.yaml` signs `artifacts: checksum`, keyless via Fulcio, so the
// release carries one `checksums.txt.sig` and one `checksums.txt.pem` and no
// per-asset signature at all. Verifying "the signature" therefore means
// verifying that pair over `checksums.txt` — after which the digests in it are
// trustworthy and the archive's own SHA-256 carries the guarantee the rest of
// the way.

// CertificateIdentityRegexp and CertificateOIDCIssuer pin whose signature is
// acceptable. They are the same two values the release footer tells a human to
// pass, and they are constants rather than flags for the reason that matters:
// a verification whose identity the caller chooses verifies nothing.
const (
	CertificateIdentityRegexp = "https://github.com/lezli01/vincent/.*"
	CertificateOIDCIssuer     = "https://token.actions.githubusercontent.com"
)

// CosignTimeout bounds the verification. cosign's keyless path talks to Rekor,
// so this is a network call and not a local hash.
const CosignTimeout = 60 * time.Second

// ErrCosignMissing is returned when no cosign binary is on PATH. It is a
// distinct error rather than a generic failure because the two outcomes
// differ: a missing cosign degrades to the checksum alone with a warning, and
// a cosign that ran and said no refuses the swap outright.
var ErrCosignMissing = errors.New("cosign is not installed")

// verifySignature checks checksums.txt against its signature and certificate.
//
// It writes the three blobs to a temp directory and shells out, because that
// is what "prefer the user's own tool" means here: cosign's verification
// semantics — Fulcio roots, Rekor inclusion, certificate policy — are its
// business, and reimplementing a subset of them against an embedded trust root
// would be a worse guarantee wearing the same name.
func (u *Updater) verifySignature(ctx context.Context, checksums, sig, cert []byte) error {
	bin := u.cosignPath
	if bin == "" {
		found, err := exec.LookPath("cosign")
		if err != nil {
			return ErrCosignMissing
		}
		bin = found
	} else if _, err := os.Stat(bin); err != nil {
		// An explicit path that is not there is the same situation as none on
		// PATH, and must reach the caller as the same error — otherwise
		// --require-signature reports a fork/exec failure instead of the one
		// thing the user can act on: install cosign.
		return ErrCosignMissing
	}
	dir, err := os.MkdirTemp("", "vincent-verify-")
	if err != nil {
		return fmt.Errorf("create verification directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	paths := map[string][]byte{
		"checksums.txt":     checksums,
		"checksums.txt.sig": sig,
		"checksums.txt.pem": cert,
	}
	for name, data := range paths {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	cctx, cancel := context.WithTimeout(ctx, CosignTimeout)
	defer cancel()
	// G204: bin is resolved from PATH or supplied by a test, and every
	// argument is a constant or a name this function just wrote into its own
	// temp directory. Nothing here comes from the release payload.
	cmd := exec.CommandContext(cctx, bin, //nolint:gosec // G204: see above
		"verify-blob", filepath.Join(dir, "checksums.txt"),
		"--certificate", filepath.Join(dir, "checksums.txt.pem"),
		"--signature", filepath.Join(dir, "checksums.txt.sig"),
		"--certificate-identity-regexp", CertificateIdentityRegexp,
		"--certificate-oidc-issuer", CertificateOIDCIssuer,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// cosign's own words, trimmed: a verification failure is the one
		// error here a user must be able to act on, and paraphrasing it
		// would drop the reason.
		return fmt.Errorf("cosign verify-blob failed: %w: %s", err, firstLine(out))
	}
	return nil
}

// firstLine keeps a subprocess's failure legible on one terminal line.
func firstLine(b []byte) string {
	s := string(b)
	for i, r := range s {
		if r == '\n' || r == '\r' {
			return s[:i]
		}
	}
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
