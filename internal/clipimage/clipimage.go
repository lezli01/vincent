package clipimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoImage is Read's one failure: there is no image on the clipboard, or
// nothing on this machine that can fetch one. A helper that is missing, that
// failed, or that returned something other than an image all report it — to
// the caller they are the same instruction, paste the text instead.
var ErrNoImage = errors.New("clipimage: no image on the clipboard")

// MaxBytes caps the image Read returns. A screenshot is a few hundred
// kilobytes; past ten mebibytes the clipboard holds something no agent CLI
// takes as an attachment, and reading it would only spend the memory.
const MaxBytes = 10 << 20

// helperTimeout bounds one helper run beneath whatever the caller's context
// allows. A clipboard helper answers in milliseconds or not at all — xclip
// waits on the selection's owner, which may be a frozen program — and a paste
// that hangs the composer is worse than one that falls back to text.
const helperTimeout = 5 * time.Second

// errTooLarge is a helper writing more than its limit. It never leaves the
// package: Read reports it as ErrNoImage with the reason attached.
var errTooLarge = errors.New("more than the limit")

// runHelper runs one clipboard helper and returns its standard output, at
// most limit bytes of it. It is a variable so tests exercise each platform's
// argument construction and output handling without a clipboard.
var runHelper = execHelper

// findHelper reports whether a helper is on PATH. A variable for the same
// reason runHelper is.
var findHelper = func(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// execHelper is runHelper for real. The process is killed when the context
// ends or when it has written more than limit; its standard error is dropped,
// because nothing a clipboard helper says is shown to anyone.
func execHelper(ctx context.Context, limit int64, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: name and args are this package's fixed helper argv, never input
	// A helper that leaves a child holding the pipe must not hold Wait.
	cmd.WaitDelay = time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// One byte past the limit is how "exactly the limit" and "more" differ.
	data, readErr := io.ReadAll(io.LimitReader(out, limit+1))
	if int64(len(data)) > limit {
		cancel()
		_ = cmd.Wait()
		return nil, errTooLarge
	}
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	return data, nil
}

// Read returns the image on the system clipboard: PNG where the platform can
// produce one, otherwise the JPEG, GIF or WebP that is there. Every way of
// not having one — no helper, no image, a helper error, an image over
// MaxBytes, the context ending — is ErrNoImage.
func Read(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	data, err := read(ctx)
	switch {
	case err == nil && sniff(data) != "" && len(data) <= MaxBytes:
		return data, nil
	case errors.Is(err, errTooLarge) || len(data) > MaxBytes:
		return nil, fmt.Errorf("%w: it is larger than %d MiB", ErrNoImage, MaxBytes>>20)
	case err != nil && !errors.Is(err, ErrNoImage):
		// The cause rides along for a log line; errors.Is still says ErrNoImage.
		return nil, fmt.Errorf("%w: %w", ErrNoImage, err)
	}
	return nil, ErrNoImage
}

// sniffLen is how much of a file says what it is: WebP's marker ends at byte
// twelve, and every other signature is shorter.
const sniffLen = 12

// sniff names the image format data begins with — png, jpeg, gif or webp —
// or "" for anything else. The formats are the ones the agent CLIs take as
// attachments; a TIFF or a BMP is not an image for this purpose.
func sniff(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "jpeg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif"
	case len(data) >= sniffLen && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	}
	return ""
}

// ImagePath reports whether text is, and is nothing but, the path of an
// image file: what a terminal pastes when a file is dragged onto it, or what
// a file manager's "copy path" leaves on the clipboard. The path it returns
// is absolute.
//
// The text is trimmed, and then read three ways, first match winning: as it
// stands; with one pair of surrounding quotes removed; and with shell
// backslash escapes removed, which is how terminals on macOS and Linux spell
// a dropped path with spaces in it. As it stands comes first so that a
// Windows path, whose backslashes are separators, is never unescaped into
// one that does not exist.
//
// It must name an existing regular file whose first bytes are a PNG, JPEG,
// GIF or WebP signature. The name's extension is not consulted: a screenshot
// tool's extensionless temp file is an image, and notes.png holding text is
// not.
func ImagePath(text string) (path string, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" || strings.ContainsAny(text, "\n\r\x00") {
		return "", false
	}
	candidates := []string{text}
	if unquoted, quoted := unquote(text); quoted {
		candidates = append(candidates, unquoted)
	}
	if strings.Contains(text, `\`) {
		candidates = append(candidates, unescape(text))
	}
	for _, c := range candidates {
		if !isImageFile(c) {
			continue
		}
		if abs, err := filepath.Abs(c); err == nil {
			return abs, true
		}
		return c, true
	}
	return "", false
}

// unquote removes one matching pair of single or double quotes around s.
func unquote(s string) (string, bool) {
	if len(s) < 2 {
		return s, false
	}
	if q := s[0]; (q == '\'' || q == '"') && s[len(s)-1] == q {
		return s[1 : len(s)-1], true
	}
	return s, false
}

// unescape removes each backslash that escapes the character after it, the
// way a POSIX shell reads an unquoted word: `My\ Shot\ \(1\).png`.
func unescape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isImageFile reports whether path is a regular file that begins as an image.
func isImageFile(path string) bool {
	// Stat first, and follow links: a symlink to an image is one, while a
	// FIFO or a device must never be opened for a paste.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path) //nolint:gosec // G304: opening the pasted path is the point; only its first bytes are read
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	return sniff(head[:n]) != ""
}
