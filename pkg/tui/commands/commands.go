// Package commands provides tea.Cmd factories for async operations.
package commands

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/kontrolplane/stdout/pkg/loki"
	"github.com/kontrolplane/stdout/pkg/tui/messages"
)

const requestTimeout = 15 * time.Second

// ValuesDelay is how long the cursor rests on a label before its values are loaded, so moving
// through the labels does not ask for the values of every one passed.
const ValuesDelay = 150 * time.Millisecond

// OlderLimit is the number of entries one load of older entries reads.
const OlderLimit = 500

// maxBatch bounds how many follow events one TailMsg takes, so a busy tail still redraws.
const maxBatch = 256

// request runs fn in a tea.Cmd with the request timeout applied to ctx.
func request(ctx context.Context, fn func(context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		return fn(ctx)
	}
}

func LoadBuildInfo(ctx context.Context, c *loki.Client) tea.Cmd {
	return request(ctx, func(ctx context.Context) tea.Msg {
		info, err := c.BuildInfo(ctx)
		return messages.BuildInfoMsg{Info: info, Err: err}
	})
}

// LoadLabels lists the label names seen in the last since.
func LoadLabels(ctx context.Context, c *loki.Client, since time.Duration) tea.Cmd {
	return request(ctx, func(ctx context.Context) tea.Msg {
		now := time.Now()
		labels, err := c.Labels(ctx, "", now.Add(-since), now)
		return messages.LabelsLoadedMsg{Labels: labels, Err: err}
	})
}

// ScheduleValues asks for the values of the label under the cursor after ValuesDelay.
func ScheduleValues(gen uint64) tea.Cmd {
	return tea.Tick(ValuesDelay, func(time.Time) tea.Msg {
		return messages.ValuesTickMsg{Gen: gen}
	})
}

// LoadValues lists the values of label seen in the last since, of the streams scope selects or
// of all of them when it is empty, with their volumes unless volumes is off.
func LoadValues(ctx context.Context, c *loki.Client, label, scope string, since time.Duration, volumes bool) tea.Cmd {
	return request(ctx, func(ctx context.Context) tea.Msg {
		now := time.Now()
		start := now.Add(-since)
		msg := messages.ValuesLoadedMsg{Label: label, Scope: scope}
		msg.Values, msg.Err = c.LabelValues(ctx, label, scope, start, now)
		if msg.Err != nil || !volumes {
			return msg
		}
		selector := label + `=~".+"`
		if scope != "" {
			selector = scope[:len(scope)-1] + ", " + selector + "}"
		} else {
			selector = "{" + selector + "}"
		}
		msg.Volumes, msg.VolumeErr = c.Volume(ctx, selector, label, start, now)
		return msg
	})
}

// Follow starts tailing query in the background, beginning with up to limit entries of the last
// since. The tail runs until ctx is done; WaitTail reads what it reports.
func Follow(ctx context.Context, c *loki.Client, query string, since time.Duration, limit int) <-chan loki.FollowEvent {
	events := make(chan loki.FollowEvent, 64)
	go c.Follow(ctx, query, time.Now().Add(-since), limit, events)
	return events
}

// WaitTail waits for the next follow event, and takes the ones already waiting behind it along,
// so a busy tail is drawn once per batch rather than once per message.
func WaitTail(events <-chan loki.FollowEvent, gen uint64) tea.Cmd {
	return func() tea.Msg {
		msg := messages.TailMsg{Gen: gen}
		e, ok := <-events
		if !ok {
			msg.Closed = true
			return msg
		}
		add := func(e loki.FollowEvent) {
			msg.Entries = append(msg.Entries, e.Batch.Entries...)
			msg.Dropped += len(e.Batch.Dropped)
			msg.State, msg.Err, msg.RetryAt = e.State, e.Err, e.RetryAt
		}
		add(e)
		for range maxBatch {
			select {
			case e, ok := <-events:
				if !ok {
					msg.Closed = true
					return msg
				}
				add(e)
				if e.State != loki.Live {
					return msg
				}
			default:
				return msg
			}
		}
		return msg
	}
}

// LoadOlder reads the newest OlderLimit entries of query in the window of since before end.
func LoadOlder(ctx context.Context, c *loki.Client, query string, end time.Time, since time.Duration, gen uint64) tea.Cmd {
	return request(ctx, func(ctx context.Context) tea.Msg {
		start := end.Add(-since)
		entries, err := c.QueryRange(ctx, query, start, end, OlderLimit, true)
		return messages.OlderLoadedMsg{Gen: gen, Start: start, End: end, Entries: entries, Err: err}
	})
}

// ScheduleClock ticks once a second while a tail runs.
func ScheduleClock(gen uint64) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return messages.ClockTickMsg{Gen: gen}
	})
}

// CopyToClipboard copies text using the system clipboard. On failure the update
// loop falls back to OSC52, which also works over ssh.
func CopyToClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		return messages.ClipboardCopiedMsg{Text: text, Err: clipboard.WriteAll(text)}
	}
}

func ClearStatusAfter(d time.Duration, gen int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return messages.StatusClearMsg{Gen: gen}
	})
}
