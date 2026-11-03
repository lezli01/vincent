package agent

import (
	"bufio"
	"bytes"
	"io"
)

// MaxLineBytes bounds one line of an agent CLI's stream. Agent messages and
// tool payloads embed file contents and aggregated command output, so a
// single line can be far past bufio's default 64 KiB token; the cap keeps a
// runaway line from growing the daemon without limit.
const MaxLineBytes = 16 * 1024 * 1024

// ReadLines scans rd line by line, with a 64 KiB initial buffer growing up to
// MaxLineBytes, and calls each with a private copy of every line that is not
// blank — each may keep the slice, since the scanner reuses its own buffer.
// A final line without a newline is delivered too.
//
// It returns nil at EOF. On a scanner error — a line over MaxLineBytes, a
// failed read — it drains the rest of rd to io.Discard before returning the
// error: a reader that stopped before the stream did would leave the CLI
// writing into a pipe nobody empties, hanging it until the step timeout
// (#139). The caller latches the error so Wait can name the failure, since
// normalization ended early and the transcript is missing lines.
func ReadLines(rd io.Reader, each func(line []byte)) error {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), MaxLineBytes)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		line := make([]byte, len(sc.Bytes()))
		copy(line, sc.Bytes())
		each(line)
	}
	if err := sc.Err(); err != nil {
		_, _ = io.Copy(io.Discard, rd)
		return err
	}
	return nil
}
