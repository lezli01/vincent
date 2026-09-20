package cli

import (
	"fmt"
	"io"
	"os"
)

// maxInputFileBytes bounds what a `path|-` input flag reads, at the same
// 4 MiB the API bounds a large request body at (§13.1). Stdin can be an
// unbounded pipe, so the read is capped rather than slurped, and nothing is
// buffered past the bound. What the bound means differs by flag:
//
//   - --fields-file (task 045): `POST /v1/tasks` is on the 4 MiB tier, so
//     refusing here gives the caller the answer the daemon would have given
//     them, sooner. Nothing is re-checked client-side beyond this — the
//     per-field bounds and the workflow's declared field contract stay
//     daemon-authoritative (§8.1.2), because the CLI is not the only client.
//   - --message-file (task 124 decision 31): `POST /v1/chats/{id}/send` is on
//     §13.1's ordinary 64 KiB tier, so this is only the guard against an
//     unbounded pipe and not the message limit. A message whose body exceeds
//     64 KiB is still refused by the daemon with `413 payload_too_large`, and
//     that refusal is the one reported.
const maxInputFileBytes = 4 << 20

// readInputFile reads the file a `path|-` flag names, or in when path is "-",
// and refuses anything over maxInputFileBytes. Errors name the flag and the
// path, never the content: a message or a field can carry a token, and an
// error message is scrollback and CI logs.
func readInputFile(flag, path string, in io.Reader) ([]byte, error) {
	src := flag + " " + path
	r := in
	if path != "-" {
		// G304: the path this command's own operator typed after the flag.
		f, err := os.Open(path) //nolint:gosec // G304: see above
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", src, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	// One byte past the bound, so input that exactly fills it is read and one
	// byte more is caught rather than silently truncated.
	data, err := io.ReadAll(io.LimitReader(r, maxInputFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", src, err)
	}
	if len(data) > maxInputFileBytes {
		return nil, fmt.Errorf("%s must be at most %d bytes", src, maxInputFileBytes)
	}
	return data, nil
}
