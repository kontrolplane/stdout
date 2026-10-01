package loki

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Matcher picks values of one label for a stream selector.
type Matcher struct {
	Label   string
	Values  []string
	Exclude bool // match streams without these values
}

// String renders the matcher in LogQL: = or != for one value, =~ or !~ for several.
func (m Matcher) String() string {
	switch len(m.Values) {
	case 0:
		return ""
	case 1:
		op := "="
		if m.Exclude {
			op = "!="
		}
		return m.Label + op + strconv.Quote(m.Values[0])
	}
	values := slices.Sorted(slices.Values(m.Values))
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = regexp.QuoteMeta(v)
	}
	op := "=~"
	if m.Exclude {
		op = "!~"
	}
	return m.Label + op + strconv.Quote(strings.Join(quoted, "|"))
}

// Selector renders matchers as a stream selector, in the order given, leaving out the ones without
// values. It returns "" when none has a value.
func Selector(matchers ...Matcher) string {
	var parts []string
	for _, m := range matchers {
		if s := m.String(); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// Selects reports whether a selector picks streams by itself. Loki refuses selectors whose every
// matcher also matches the empty value, like {app!="api"}, so at least one must include a value.
func Selects(matchers ...Matcher) bool {
	for _, m := range matchers {
		if len(m.Values) > 0 && !m.Exclude && !slices.Contains(m.Values, "") {
			return true
		}
	}
	return false
}

// StreamSelector is the selector that picks exactly the stream with these labels.
func StreamSelector(labels Labels) string {
	names := slices.Sorted(maps.Keys(labels))
	matchers := make([]Matcher, len(names))
	for i, name := range names {
		matchers[i] = Matcher{Label: name, Values: []string{labels[name]}}
	}
	return Selector(matchers...)
}

// WithLineFilter appends a line filter for text to a query, or returns it as is for empty text.
func WithLineFilter(query, text string) string {
	if text == "" {
		return query
	}
	return query + " |= " + strconv.Quote(text)
}
