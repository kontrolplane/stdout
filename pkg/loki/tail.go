package loki

import (
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

	"github.com/coder/websocket"
)

// TailBatch is one message of the live tail: the entries it carries, oldest first, and the ones
// the server dropped because the client fell behind.
type TailBatch struct {
	Entries []Entry
	Dropped []Dropped
}

type tailResponse struct {
	Streams []stream `json:"streams"`
	Dropped []struct {
		Labels    Labels `json:"labels"`
		Timestamp string `json:"timestamp"`
	} `json:"dropped_entries"`
}

// RefusedError is the server closing a tail with a reason before it sent anything, which is how
// Loki turns down a query it accepted the websocket for, like a metric query.
type RefusedError struct {
	Reason string
}

func (e *RefusedError) Error() string { return e.Reason }

// ErrTailClosed is returned when the server ends the tail, which it does once tail_max_duration
// has passed.
var ErrTailClosed = errors.New("the server closed the tail")

// Tail follows the entries query selects, starting with up to limit entries since start, and
// calls fn with every batch until ctx is done or the connection fails. opened, when set, is called
// once the server accepted the tail. It needs a websocket to the server; a gateway that does not
// pass them on answers the upgrade with an error status.
func (c *Client) Tail(ctx context.Context, query string, start time.Time, limit int, opened func(), fn func(TailBatch)) error {
	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("delay_for", "0")
	if !start.IsZero() {
		q.Set("start", nanos(start))
	}
	u := c.endpoint("/loki/api/v1/tail", q)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}

	header := c.header.Clone()
	if header == nil {
		header = http.Header{}
	}
	header.Set("X-Loki-Response-Encoding-Flags", "categorize-labels")
	conn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient: c.http,
		HTTPHeader: header,
	})
	if err != nil {
		if resp != nil && resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusSwitchingProtocols {
			if resp.Body == nil {
				resp.Body = io.NopCloser(strings.NewReader(""))
			}
			return statusError(resp)
		}
		return err
	}
	defer func() { _ = conn.CloseNow() }()
	// A batch of history can be large, the default limit of 32KiB would end the tail on it.
	conn.SetReadLimit(64 << 20)
	if opened != nil {
		opened()
	}

	received := false
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var closed websocket.CloseError
			if errors.As(err, &closed) {
				if closed.Code == websocket.StatusNormalClosure {
					return ErrTailClosed
				}
				if closed.Reason != "" && !received {
					return &RefusedError{Reason: closed.Reason}
				}
				if closed.Reason != "" {
					return errors.New(closed.Reason)
				}
			}
			return err
		}
		batch, err := decodeTail(data)
		if err != nil {
			return err
		}
		received = true
		fn(batch)
	}
}

func decodeTail(data []byte) (TailBatch, error) {
	var resp tailResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return TailBatch{}, fmt.Errorf("decoding the tail: %w", err)
	}
	var batch TailBatch
	for _, s := range resp.Streams {
		e, err := s.entries()
		if err != nil {
			return TailBatch{}, err
		}
		batch.Entries = append(batch.Entries, e...)
	}
	sortEntries(batch.Entries)
	for _, d := range resp.Dropped {
		t, err := parseNanos(d.Timestamp)
		if err != nil {
			continue
		}
		batch.Dropped = append(batch.Dropped, Dropped{Time: t, Labels: d.Labels})
	}
	return batch, nil
}
