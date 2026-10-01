package loki

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// FollowState is where a follow stands with the server.
type FollowState int

const (
	Connecting FollowState = iota // dialing, the first time
	Live                          // the tail is open
	Retrying                      // the tail failed and is dialed again at RetryAt
	Failed                        // the server refused the tail, it is not tried again
)

// FollowEvent is what Follow reports: entries, or a change of state.
type FollowEvent struct {
	State   FollowState
	Batch   TailBatch
	Err     error     // why the tail failed, when Retrying or Failed
	RetryAt time.Time // when the tail is dialed again, when Retrying
}

const (
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// resumeLimit is how many entries a tail opened again sends of what was missed.
	resumeLimit = 5000
)

// Follow tails query for as long as ctx lives, dialing again when the connection drops, and sends
// what happens on events. It starts with up to limit entries since start. A tail opened again
// resumes after the last entry seen, so entries are neither missed, up to resumeLimit, nor sent
// twice. Follow closes events when it returns.
func (c *Client) Follow(ctx context.Context, query string, start time.Time, limit int, events chan<- FollowEvent) {
	defer close(events)
	send := func(e FollowEvent) bool {
		select {
		case events <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}

	var d dedupe
	backoff := minBackoff
	worked := false // the tail has sent entries, so the query itself is fine
	for {
		err := c.Tail(ctx, query, start, limit,
			func() { send(FollowEvent{State: Live}) },
			func(b TailBatch) {
				backoff = minBackoff
				worked = true
				b.Entries = d.filter(b.Entries)
				if len(b.Entries) > 0 || len(b.Dropped) > 0 {
					send(FollowEvent{State: Live, Batch: b})
				}
			})
		if ctx.Err() != nil {
			return
		}
		if fatal(err, worked) {
			send(FollowEvent{State: Failed, Err: err})
			return
		}
		retryAt := time.Now().Add(backoff)
		if errors.Is(err, ErrTailClosed) {
			// The server ends every tail after tail_max_duration, that is not a failure.
			retryAt = time.Now()
		}
		if !send(FollowEvent{State: Retrying, Err: err, RetryAt: retryAt}) {
			return
		}
		select {
		case <-time.After(time.Until(retryAt)):
		case <-ctx.Done():
			return
		}
		backoff = min(2*backoff, maxBackoff)
		if !d.last.IsZero() {
			start, limit = d.last, resumeLimit
		}
	}
}

// fatal reports whether err is the server refusing the tail, as for a query it cannot parse, so
// dialing again would get the same answer. A tail that worked before and is turned down now, say
// for having too many tails open, is tried again.
func fatal(err error, worked bool) bool {
	var refused *RefusedError
	if errors.As(err, &refused) {
		return !worked
	}
	var s *StatusError
	if !errors.As(err, &s) {
		return false
	}
	return s.Code/100 == 4 && s.Code != http.StatusTooManyRequests && s.Code != http.StatusRequestTimeout
}

// dedupe drops the entries a tail opened again sends a second time. It resumes at the time of
// the last entry, so the entries at that very time come again.
type dedupe struct {
	last time.Time
	seen map[string]bool // keys of the entries at last
}

func (d *dedupe) filter(entries []Entry) []Entry {
	out := entries[:0]
	for _, e := range entries {
		switch {
		case e.Time.After(d.last):
			d.last = e.Time
			d.seen = map[string]bool{e.Key(): true}
		case e.Time.Equal(d.last):
			k := e.Key()
			if d.seen[k] {
				continue
			}
			d.seen[k] = true
		}
		// An entry older than the last one is late rather than repeated: the tail only goes back
		// to the last entry, so it cannot have been sent before.
		out = append(out, e)
	}
	return out
}
