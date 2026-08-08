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
	"strings"
	"time"
)

const (
	// defaultInitialBackoff is the first reconnect delay; loopback comes back
	// fast, so start eager.
	defaultInitialBackoff = 250 * time.Millisecond
	// defaultMaxBackoff caps the reconnect delay while the daemon is down.
	defaultMaxBackoff = 5 * time.Second
	// maxFrameSize bounds one SSE line; payloads are ids + small JSON, but a
	// diff-bearing future payload should not kill the stream.
	maxFrameSize = 1 << 20
)

// Event is one durable state event as framed on the wire (§13.3): the full
// envelope, so no side channel is needed to interpret it.
type Event struct {
	ID        int64           `json:"id"`
	TS        time.Time       `json:"ts"`
	Type      string          `json:"type"`
	TaskID    *int64          `json:"task_id,omitempty"`
	ProjectID *int64          `json:"project_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

// Note is one item on an event stream: an event, or a connection-state
// transition the UI renders.
type Note interface{ isNote() }

// EventNote delivers one durable event.
type EventNote struct{ Event Event }

// ConnectedNote reports a (re)established stream. Cursor is the resume
// position the connection asked for (0 = live-only).
type ConnectedNote struct{ Cursor int64 }

// DisconnectedNote reports a dropped stream; the subscription retries by
// itself after RetryIn.
type DisconnectedNote struct {
	Err     error
	RetryIn time.Duration
}

func (EventNote) isNote()        {}
func (ConnectedNote) isNote()    {}
func (DisconnectedNote) isNote() {}

// StreamOptions filter and position a durable-event subscription (§13.3).
type StreamOptions struct {
	// Types filters to these event types (server-side, ?types=).
	Types []string
	// ProjectID filters to one project (server-side, ?project_id=).
	ProjectID int64
	// LastEventID resumes after this event id; 0 starts live at the next
	// committed event (the stream never replays history unasked).
	LastEventID int64
	// InitialBackoff/MaxBackoff tune reconnect pacing; zero means defaults.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// StreamEvents subscribes to GET /v1/events and delivers durable events on
// the returned channel. The subscription reconnects forever with capped
// backoff, resuming via Last-Event-ID so no durable event is lost; it stops
// and closes the channel only when ctx is canceled.
func (c *Client) StreamEvents(ctx context.Context, opts StreamOptions) <-chan Note {
	ch := make(chan Note)
	go c.streamLoop(ctx, opts, ch)
	return ch
}

func (c *Client) streamLoop(ctx context.Context, opts StreamOptions, ch chan<- Note) {
	defer close(ch)
	initial := opts.InitialBackoff
	if initial <= 0 {
		initial = defaultInitialBackoff
	}
	maxB := opts.MaxBackoff
	if maxB <= 0 {
		maxB = defaultMaxBackoff
	}
	cursor := opts.LastEventID
	backoff := initial
	for {
		connected, err := c.streamOnce(ctx, opts, &cursor, ch)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = initial // the connection worked; the drop is fresh news
		}
		if !send(ctx, ch, DisconnectedNote{Err: err, RetryIn: backoff}) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxB)
	}
}

// streamOnce runs one connection: dial, announce, then decode frames until
// the stream breaks. connected reports whether the dial reached streaming.
func (c *Client) streamOnce(
	ctx context.Context, opts StreamOptions, cursor *int64, ch chan<- Note,
) (connected bool, err error) {
	req, err := c.buildStreamRequest(ctx, opts, *cursor)
	if err != nil {
		return false, err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return false, fmt.Errorf("connect event stream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, decodeError(resp)
	}
	if !send(ctx, ch, ConnectedNote{Cursor: *cursor}) {
		return true, ctx.Err()
	}
	return true, c.readFrames(ctx, resp.Body, cursor, ch)
}

func (c *Client) buildStreamRequest(
	ctx context.Context, opts StreamOptions, cursor int64,
) (*http.Request, error) {
	q := url.Values{}
	if len(opts.Types) > 0 {
		q.Set("types", strings.Join(opts.Types, ","))
	}
	if opts.ProjectID != 0 {
		q.Set("project_id", strconv.FormatInt(opts.ProjectID, 10))
	}
	u := c.baseURL + "/v1/events"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build event stream request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/event-stream")
	if cursor > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(cursor, 10))
	}
	return req, nil
}

// readFrames decodes SSE frames until the body ends. Comment lines
// (heartbeats) are skipped; a frame dispatches on its blank line, with
// multiple data: lines joined by newlines per the SSE spec.
func (c *Client) readFrames(
	ctx context.Context, body io.Reader, cursor *int64, ch chan<- Note,
) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), maxFrameSize)
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				if !c.dispatch(ctx, strings.Join(data, "\n"), cursor, ch) {
					return ctx.Err()
				}
				data = data[:0]
			}
		case strings.HasPrefix(line, ":"): // comment/heartbeat
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default: // id:/event: fields — the data envelope repeats both
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read event stream: %w", err)
	}
	return errors.New("event stream ended")
}

// dispatch decodes one frame body and delivers it. Frames without an id
// (live-output chunks on per-task streams) never advance the resume cursor.
func (c *Client) dispatch(ctx context.Context, data string, cursor *int64, ch chan<- Note) bool {
	var ev Event
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		// A malformed frame is a server bug; skipping it beats killing the
		// stream and replaying everything.
		return true
	}
	if ev.ID > 0 {
		*cursor = ev.ID
	}
	return send(ctx, ch, EventNote{Event: ev})
}

// send delivers n unless ctx ends first; it reports whether delivery happened.
func send(ctx context.Context, ch chan<- Note, n Note) bool {
	select {
	case <-ctx.Done():
		return false
	case ch <- n:
		return true
	}
}
