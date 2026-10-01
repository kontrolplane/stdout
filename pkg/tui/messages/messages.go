// Package messages holds the tea.Msg types the commands answer with.
package messages

import (
	"time"

	"github.com/kontrolplane/stdout/pkg/loki"
)

// BuildInfoMsg carries the version of the server.
type BuildInfoMsg struct {
	Info loki.BuildInfo
	Err  error
}

// LabelsLoadedMsg carries the label names of the picker's range.
type LabelsLoadedMsg struct {
	Labels []string
	Err    error
}

// ValuesTickMsg asks for the values of a label once the cursor has rested on it.
type ValuesTickMsg struct {
	Gen uint64
}

// ValuesLoadedMsg carries the values of a label, of the streams scope selects, with the bytes
// each value ingested when the server reports volumes.
type ValuesLoadedMsg struct {
	Label     string
	Scope     string
	Values    []string
	Volumes   map[string]uint64
	VolumeErr error
	Err       error
}

// TailMsg carries what a follow reported since the last one: its entries, oldest first, the
// number of entries the server dropped, and the latest state. Closed is set once the follow ended.
type TailMsg struct {
	Gen     uint64
	Entries []loki.Entry
	Dropped int
	State   loki.FollowState
	Err     error
	RetryAt time.Time
	Closed  bool
}

// OlderLoadedMsg carries the entries before End, for scrolling past the top of the tail.
type OlderLoadedMsg struct {
	Gen     uint64
	Start   time.Time
	End     time.Time
	Entries []loki.Entry
	Err     error
}

// ClockTickMsg redraws what changes with the time, like the rate and a retry countdown.
type ClockTickMsg struct {
	Gen uint64
}

// ClipboardCopiedMsg reports a copy to the system clipboard.
type ClipboardCopiedMsg struct {
	Text string
	Err  error
}

// StatusClearMsg clears the footer status, unless a newer one replaced it.
type StatusClearMsg struct {
	Gen int
}
