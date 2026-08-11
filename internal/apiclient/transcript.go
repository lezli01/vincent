package apiclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// DefaultTailBytes is how much of a transcript a fresh view asks for. A few
// thousand lines of output — far more than a screenful of scrollback, small
// enough to decode instantly over loopback — instead of a file that §18
// allows to reach gigabytes.
const DefaultTailBytes = 256 * 1024

// TranscriptRecord is one line of a normalized transcript (§13.2). The
// vocabulary spans two families — the `agent.*`/`command.output` shapes the
// live stream also publishes, and vincent's own `vincent.*` annotations — so
// the fields are a union and Type says which of them mean anything.
type TranscriptRecord struct {
	Type string `json:"type"`
	// Text is the human-readable line of agent.output, command.output and
	// vincent.output.
	Text  string           `json:"text"`
	Tools []TranscriptTool `json:"tools"`
	// Phase and Stream tag command output: which command produced it (the
	// step's own or its check) and whether it came from stdout or stderr.
	Phase  string `json:"phase"`
	Stream string `json:"stream"`
	// Message carries an error's text; Kind and Summary describe an input
	// request the run stopped for.
	Message string `json:"message"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	// Line is the verbatim source line of an agent.raw record — a line the
	// adapter's dialect did not recognize, kept rather than dropped.
	Line       string `json:"line"`
	ResultText string `json:"result_text"`
	IsError    bool   `json:"is_error"`
	// Raw is the whole record, for the annotation fields this struct does not
	// name.
	Raw json.RawMessage `json:"-"`
}

// TranscriptTool is one tool invocation inside an agent.tool_use record.
// Summary is the call's subject — the command run, the file edited — and is
// empty when the dialect's arguments carried nothing recognizable. CallID
// correlates the call with the agent.tool_result reporting its outcome; it
// is what makes that pairing correct when an agent runs tools in parallel.
type TranscriptTool struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	CallID  string `json:"call_id"`
}

// TranscriptOptions selects a byte range of one attempt's transcript. Offset
// and Tail are mutually exclusive; both resolve to record boundaries
// server-side, so a record is never returned in halves.
type TranscriptOptions struct {
	// Offset resumes at a byte position, normally a previous NextOffset.
	Offset int64
	// Tail asks for roughly the last N bytes instead, opening at the start of
	// the record that position lands in.
	Tail int64
}

func (o TranscriptOptions) query() string {
	q := url.Values{"format": {"normalized"}}
	switch {
	case o.Tail > 0:
		q.Set("tail", strconv.FormatInt(o.Tail, 10))
	case o.Offset > 0:
		q.Set("offset", strconv.FormatInt(o.Offset, 10))
	}
	return "?" + q.Encode()
}

// Transcript fetches one attempt's transcript in normalized form, returning
// the records and the offset to resume from. The resume offset is always a
// record boundary, so following a live attempt is: fetch, then keep every
// chunk whose offset is past NextOffset.
func (c *Client) Transcript(
	ctx context.Context, taskID, runID int64, opts TranscriptOptions,
) (records []TranscriptRecord, nextOffset int64, err error) {
	path := fmt.Sprintf("/v1/tasks/%d/steps/%d/transcript%s", taskID, runID, opts.query())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build transcript request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.rest.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("GET transcript: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, 0, decodeError(resp)
	}
	if v := resp.Header.Get("X-Next-Offset"); v != "" {
		nextOffset, _ = strconv.ParseInt(v, 10, 64)
	}
	records, err = decodeTranscript(resp.Body)
	return records, nextOffset, err
}

// decodeTranscript reads NDJSON records, tolerating a record it cannot parse
// rather than losing the whole fetch to one bad line.
func decodeTranscript(body io.Reader) ([]TranscriptRecord, error) {
	var out []TranscriptRecord
	br := bufio.NewReader(body)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var rec TranscriptRecord
			if json.Unmarshal(line, &rec) == nil && rec.Type != "" {
				rec.Raw = json.RawMessage(line)
				out = append(out, rec)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, fmt.Errorf("read transcript: %w", err)
		}
	}
}
