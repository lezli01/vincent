package taskrun

import (
	"bufio"
	"errors"
	"io"
)

// outputChunkBytes bounds one captured output record (#139).
//
// A `command` step's output is recorded a line at a time, but a line is not a
// bound: minified JSON, a base64 blob and a `git diff` of a generated file all
// reach a megabyte on one line. A line longer than this becomes a run of
// records rather than a capture failure — see scanOutput.
//
// The size is chosen so that everything downstream keeps reading records the
// way it always has: it is well inside the live-chunk shapes of §13.3 and the
// buffers the API's transcript reader works in.
const outputChunkBytes = 64 * 1024

// scanOutput reads rd to the end and calls emit once per output record.
//
// A line shorter than outputChunkBytes produces one record with more=false,
// exactly as a line-oriented scanner produced. A longer line produces a run of
// records, each at most outputChunkBytes, all but the last with more=true:
// they are pieces of one line, in order, on one stream. Nothing is dropped and
// nothing is truncated.
//
// It replaces a `bufio.Scanner` capped at one mebibyte per token, which did
// not truncate an over-long line — it stopped capture dead on the first one
// and discarded the rest of the stream, while the step still reported success
// from its exit code (#139, §7.1).
//
// The trailing "\r" of a CRLF is dropped, the way `bufio.ScanLines` did: a
// command step runs under pwsh on Windows (§8.3). A "\r" that lands on a chunk
// boundary is held back rather than emitted, because the "\n" that would make
// it a line ending is in the next read.
//
// The returned error is a read failure on rd — the stream ended early and
// output that the process wrote was never seen. Reaching io.EOF is not one.
func scanOutput(rd io.Reader, emit func(text string, more bool)) error {
	br := bufio.NewReaderSize(rd, outputChunkBytes)
	var line []byte
	for {
		frag, err := br.ReadSlice('\n')
		switch {
		case err == nil:
			line = append(line, dropCR(frag[:len(frag)-1])...)
			emit(string(line), false)
			line = line[:0]
		case errors.Is(err, bufio.ErrBufferFull):
			// No newline in a whole buffer: this is a piece of a long line.
			line = append(line, frag...)
			keepCR := line[len(line)-1] == '\r'
			if keepCR {
				line = line[:len(line)-1]
			}
			emit(string(line), true)
			line = line[:0]
			if keepCR {
				line = append(line, '\r')
			}
		default:
			// A last line with no terminator is still a line.
			line = append(line, frag...)
			if len(line) > 0 {
				emit(string(dropCR(line)), false)
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// dropCR trims the "\r" of a CRLF line ending.
func dropCR(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\r' {
		return b[:len(b)-1]
	}
	return b
}
