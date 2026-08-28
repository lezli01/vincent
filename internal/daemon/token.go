package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const tokenFile = "token"

// TokenPath returns the API bearer token file path inside dataDir.
func TokenPath(dataDir string) string { return filepath.Join(dataDir, tokenFile) }

// ReadToken reads the API bearer token created by a previous daemon start.
func ReadToken(dataDir string) (string, error) {
	b, err := os.ReadFile(TokenPath(dataDir))
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errors.New("read token: file is empty")
	}
	return tok, nil
}

// EnsureToken returns the existing bearer token or generates one on first
// start: 32 bytes of crypto/rand as hex, written atomically with 0600
// permissions (spec §13.1). An existing token is reused unchanged; on POSIX
// its permissions are re-tightened to 0600.
func EnsureToken(dataDir string) (string, error) {
	path := TokenPath(dataDir)
	b, err := os.ReadFile(path) //nolint:gosec // G304: TokenPath() under the daemon's own data dir
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read token: %w", err)
	}
	if tok := strings.TrimSpace(string(b)); err == nil && tok != "" {
		if runtime.GOOS != "windows" {
			if err := os.Chmod(path, 0o600); err != nil {
				return "", fmt.Errorf("tighten token permissions: %w", err)
			}
		}
		return tok, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	tok := hex.EncodeToString(buf)
	if err := writeFileAtomic(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token: %w", err)
	}
	return tok, nil
}

// writeFileAtomic writes data to a temp file in path's directory and renames
// it into place, so readers never observe a partial file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := f.Name()
	cleanup := func() { _ = f.Close(); _ = os.Remove(tmp) }
	if err := f.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
