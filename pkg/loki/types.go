// Package loki talks to the Loki HTTP API: labels and their values, log queries and the live tail.
package loki

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Labels is a set of label names and values, of a stream or of an entry.
type Labels map[string]string

// String renders the labels as a LogQL stream selector, sorted by name.
func (l Labels) String() string {
	names := slices.Sorted(maps.Keys(l))
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name + "=" + strconv.Quote(l[name])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// Entry is one log line with the stream it belongs to.
type Entry struct {
	Time   time.Time
	Line   string
	Labels Labels // the labels of the stream, as indexed
	// Metadata and Parsed are only told apart from the stream labels when the server categorizes
	// them, which Loki 3 does. Older servers fold them into Labels.
	Metadata Labels // structured metadata attached to the line
	Parsed   Labels // labels extracted by a parser stage of the query
}

// Key identifies an entry: two entries with the same time, line and stream are the same entry.
func (e Entry) Key() string {
	return strconv.FormatInt(e.Time.UnixNano(), 10) + "\x00" + e.Labels.String() + "\x00" + e.Line
}

// Dropped is an entry the server left out of the tail because the client did not keep up.
type Dropped struct {
	Time   time.Time
	Labels Labels
}

// BuildInfo is what the server reports about its build.
type BuildInfo struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	Branch    string `json:"branch"`
	GoVersion string `json:"goVersion"`
}

// stream is a stream of entries as the API returns it, for a query or the tail.
type stream struct {
	Stream Labels            `json:"stream"`
	Values []json.RawMessage `json:"values"`
}

// categories holds the labels the server sets apart from the stream labels when asked to with
// the categorize-labels encoding flag.
type categories struct {
	StructuredMetadata Labels `json:"structuredMetadata"`
	Parsed             Labels `json:"parsed"`
}

// entries decodes the values of a stream. A value is [timestamp, line], or with categorized
// labels [timestamp, line, {structuredMetadata, parsed}].
func (s stream) entries() ([]Entry, error) {
	out := make([]Entry, 0, len(s.Values))
	for _, raw := range s.Values {
		var parts []json.RawMessage
		if err := json.Unmarshal(raw, &parts); err != nil {
			return nil, fmt.Errorf("decoding entry: %w", err)
		}
		if len(parts) < 2 {
			return nil, fmt.Errorf("decoding entry: expected a timestamp and a line, got %d values", len(parts))
		}
		var ts, line string
		if err := json.Unmarshal(parts[0], &ts); err != nil {
			return nil, fmt.Errorf("decoding entry timestamp: %w", err)
		}
		if err := json.Unmarshal(parts[1], &line); err != nil {
			return nil, fmt.Errorf("decoding entry line: %w", err)
		}
		t, err := parseNanos(ts)
		if err != nil {
			return nil, err
		}
		e := Entry{Time: t, Line: line, Labels: s.Stream}
		if len(parts) > 2 {
			var c categories
			if err := json.Unmarshal(parts[2], &c); err == nil {
				e.Metadata = c.StructuredMetadata
				e.Parsed = c.Parsed
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func parseNanos(s string) (time.Time, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("decoding timestamp %q: %w", s, err)
	}
	return time.Unix(0, n), nil
}

func nanos(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}

// sortEntries orders entries by time, oldest first, keeping the order of entries with equal times.
func sortEntries(entries []Entry) {
	slices.SortStableFunc(entries, func(a, b Entry) int { return a.Time.Compare(b.Time) })
}
