package loki

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"unicode"
)

// Level is the severity of a log line, as far as it can be told.
type Level int

const (
	LevelUnknown Level = iota
	LevelTrace
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

func (l Level) String() string {
	switch l {
	case LevelTrace:
		return "trace"
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	case LevelFatal:
		return "fatal"
	}
	return ""
}

// ParseLevel reads a level as loggers write it: "WARNING", "err", "I", "50", ...
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace", "trc", "t", "10":
		return LevelTrace
	case "debug", "dbg", "d", "20":
		return LevelDebug
	case "info", "inf", "information", "informational", "notice", "i", "30":
		return LevelInfo
	case "warn", "warning", "wrn", "w", "40":
		return LevelWarn
	case "error", "err", "eror", "e", "50":
		return LevelError
	case "fatal", "ftl", "critical", "crit", "crt", "panic", "emerg", "emergency", "alert", "f", "c", "60":
		return LevelFatal
	}
	return LevelUnknown
}

// levelKeys are the labels and fields loggers put the level in, the most specific first.
var levelKeys = []string{"level", "detected_level", "lvl", "severity", "loglevel", "log_level", "severity_text"}

// DetectLevel finds the level of an entry: in its labels, its structured metadata, its fields,
// or a level word near the start of the line.
func DetectLevel(e Entry) Level {
	for _, set := range []Labels{e.Parsed, e.Metadata, e.Labels} {
		for _, k := range levelKeys {
			if v, ok := set[k]; ok {
				if l := ParseLevel(v); l != LevelUnknown {
					return l
				}
			}
		}
	}
	if l, ok := jsonLevel(e.Line); ok {
		if l != LevelUnknown {
			return l
		}
	} else if fields := logfmt(e.Line); fields != nil {
		for _, k := range levelKeys {
			for _, f := range fields {
				if strings.EqualFold(f.Key, k) {
					if l := ParseLevel(f.Value); l != LevelUnknown {
						return l
					}
				}
			}
		}
	}
	return levelWord(e.Line)
}

// jsonLevel finds the level in the top level fields of a JSON object, without decoding the rest of
// it: every line of a tail goes through here. It reports false for a line that is not an object,
// and LevelUnknown for an object without a level, which then has none of its own.
func jsonLevel(line string) (Level, bool) {
	level, rank := LevelUnknown, len(levelKeys)
	ok := scanObject(line, func(key, value string) {
		for i, k := range levelKeys[:rank] {
			if !strings.EqualFold(key, k) {
				continue
			}
			if strings.HasPrefix(value, `"`) {
				var s string
				if json.Unmarshal([]byte(value), &s) != nil {
					return
				}
				value = s
			}
			if l := ParseLevel(value); l != LevelUnknown {
				level, rank = l, i
			}
			return
		}
	})
	return level, ok
}

// scanObject calls fn with every top level key of a JSON object and its value as written, and
// reports whether s is an object. Keys with escapes are passed on as written too, no level key
// needs one.
func scanObject(s string, fn func(key, value string)) bool {
	i := skipSpace(s, 0)
	if i >= len(s) || s[i] != '{' {
		return false
	}
	i = skipSpace(s, i+1)
	if i < len(s) && s[i] == '}' {
		return skipSpace(s, i+1) == len(s)
	}
	for i < len(s) {
		if s[i] != '"' {
			return false
		}
		end := skipValue(s, i)
		if end < 0 {
			return false
		}
		key := s[i+1 : end-1]
		i = skipSpace(s, end)
		if i >= len(s) || s[i] != ':' {
			return false
		}
		i = skipSpace(s, i+1)
		end = skipValue(s, i)
		if end < 0 {
			return false
		}
		fn(key, s[i:end])
		i = skipSpace(s, end)
		if i >= len(s) {
			return false
		}
		switch s[i] {
		case '}':
			return skipSpace(s, i+1) == len(s)
		case ',':
			i = skipSpace(s, i+1)
		default:
			return false
		}
	}
	return false
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// skipValue returns where the JSON value starting at i ends, or -1 when it does not.
func skipValue(s string, i int) int {
	if i >= len(s) {
		return -1
	}
	switch s[i] {
	case '"':
		for j := i + 1; j < len(s); j++ {
			switch s[j] {
			case '\\':
				j++
			case '"':
				return j + 1
			}
		}
		return -1
	case '{', '[':
		depth := 0
		for j := i; j < len(s); j++ {
			switch s[j] {
			case '"':
				end := skipValue(s, j)
				if end < 0 {
					return -1
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	}
	j := i
	for j < len(s) && strings.IndexByte(",}] \t\n\r", s[j]) < 0 {
		j++
	}
	if j == i {
		return -1
	}
	return j
}

// levelWord finds a level written as a word of its own in the first part of a line, as in
// "2026-10-01 12:00:00 [WARN] disk almost full". Single letters are not taken, they are too often
// something else.
func levelWord(line string) Level {
	if len(line) > 80 {
		line = line[:80]
	}
	words := strings.FieldsFunc(line, func(r rune) bool { return !unicode.IsLetter(r) })
	for _, w := range words {
		if len(w) < 3 || strings.ToUpper(w) != w {
			continue
		}
		if l := ParseLevel(w); l != LevelUnknown {
			return l
		}
	}
	return LevelUnknown
}

// Field is a key and value read from a structured log line.
type Field struct {
	Key   string
	Value string
}

// Format names how a line is structured.
func Format(line string) string {
	switch {
	case isJSONObject(line):
		return "json"
	case len(logfmt(line)) > 0:
		return "logfmt"
	}
	return "text"
}

// Fields reads the top level fields of a JSON object or a logfmt line, in the order they are
// written. It returns nil for a line that is neither.
func Fields(line string) []Field {
	if isJSONObject(line) {
		return jsonFields(line)
	}
	return logfmt(line)
}

func isJSONObject(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "{") && json.Valid([]byte(t))
}

// jsonFields reads the top level fields of a JSON object in order. Values that are objects or
// arrays are kept as compact JSON.
func jsonFields(line string) []Field {
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var fields []Field
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return fields
		}
		k, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return fields
		}
		var v string
		if json.Unmarshal(raw, &v) != nil {
			var b bytes.Buffer
			if json.Compact(&b, raw) == nil {
				v = b.String()
			} else {
				v = string(raw)
			}
		}
		fields = append(fields, Field{Key: k, Value: v})
	}
	return fields
}

// logfmt reads key=value pairs. A line counts as logfmt when at least two of its words are pairs
// and they make up most of it, so prose with an = in it is not taken for fields.
func logfmt(line string) []Field {
	var fields []Field
	words := 0
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		words++
		start := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' && line[i] != '"' {
			i++
		}
		key := line[start:i]
		if i >= len(line) || line[i] != '=' || key == "" {
			for i < len(line) && line[i] != ' ' {
				i++
			}
			continue
		}
		i++ // =
		var value string
		if i < len(line) && line[i] == '"' {
			j := i + 1
			var b strings.Builder
			for j < len(line) && line[j] != '"' {
				if line[j] == '\\' && j+1 < len(line) {
					j++
				}
				b.WriteByte(line[j])
				j++
			}
			value = b.String()
			i = min(j+1, len(line))
		} else {
			j := i
			for j < len(line) && line[j] != ' ' {
				j++
			}
			value = line[i:j]
			i = j
		}
		fields = append(fields, Field{Key: key, Value: value})
	}
	if len(fields) < 2 || len(fields)*3 < words*2 {
		return nil
	}
	return fields
}

// Pretty indents a JSON line, or returns the line as it is.
func Pretty(line string) (string, bool) {
	if !isJSONObject(line) && !strings.HasPrefix(strings.TrimSpace(line), "[") {
		return line, false
	}
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(strings.TrimSpace(line)), "", "  "); err != nil {
		return line, false
	}
	return b.String(), true
}

// VaryingLabels returns the names of the labels whose values differ between the streams, or that
// some streams lack, sorted. Labels every stream shares say nothing about a line.
func VaryingLabels(streams []Labels) []string {
	if len(streams) < 2 {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, s := range streams {
		for name := range s {
			if seen[name] {
				continue
			}
			seen[name] = true
			first, ok := streams[0][name]
			for _, other := range streams[1:] {
				v, has := other[name]
				if has != ok || v != first {
					names = append(names, name)
					break
				}
			}
		}
	}
	slices.Sort(names)
	// A label that only repeats another, like the service_name Loki 3 copies from app, says nothing
	// that one does not.
	var kept []string
	for _, name := range names {
		if !slices.ContainsFunc(kept, func(other string) bool { return mirrors(streams, name, other) }) {
			kept = append(kept, name)
		}
	}
	return kept
}

// mirrors reports whether label a has the same value as label b in every stream.
func mirrors(streams []Labels, a, b string) bool {
	for _, s := range streams {
		va, oka := s[a]
		vb, okb := s[b]
		if oka != okb || va != vb {
			return false
		}
	}
	return true
}

// Describe sums up a query error for the interface: Loki wraps parse errors in a lot of detail.
func Describe(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "parse error"); i > 0 {
		msg = msg[i:]
	}
	return strings.TrimSpace(msg)
}
