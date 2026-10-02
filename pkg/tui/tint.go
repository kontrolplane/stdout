package tui

import (
	"strings"

	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

// tint splits a line of the tail into spans so the structure of JSON and logfmt recedes behind
// the values: keys and punctuation are faint, values keep the body ink, and the message, what a
// line is read for, the text ink. Anything else reads as it is, with a leading timestamp, which
// the time column already shows, faint.
func tint(line string) cell {
	if t := strings.TrimLeft(line, " "); strings.HasPrefix(t, "{") {
		return tintJSON(line)
	}
	if c, ok := tintLogfmt(line); ok {
		return c
	}
	if n := leadingTimestamp(line); n > 0 {
		return cell{{Text: line[:n], Tone: styles.ToneFaint}, {Text: line[n:], Tone: styles.ToneBody}}
	}
	return cell{{Text: line, Tone: styles.ToneBody}}
}

// messageKeys are the fields loggers write the message in.
var messageKeys = []string{"msg", "message", "error", "err"}

func isMessageKey(key string) bool {
	for _, k := range messageKeys {
		if strings.EqualFold(key, k) {
			return true
		}
	}
	return false
}

// tintJSON tints a JSON line. It does not check the line is valid, a broken one is tinted as far
// as it reads.
func tintJSON(line string) cell {
	var c cell
	add := func(text string, tone styles.Tone) {
		if text == "" {
			return
		}
		if n := len(c); n > 0 && c[n-1].Tone == tone {
			c[n-1].Text += text
			return
		}
		c = append(c, styles.Span{Text: text, Tone: tone})
	}
	depth := 0
	key := ""
	for i := 0; i < len(line); {
		switch ch := line[i]; {
		case ch == '"':
			j := i + 1
			for j < len(line) && line[j] != '"' {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(line))
			k := j
			for k < len(line) && line[k] == ' ' {
				k++
			}
			switch {
			case k < len(line) && line[k] == ':':
				key = line[i+1 : max(i+1, j-1)]
				add(line[i:j], styles.ToneFaint)
			case depth == 1 && isMessageKey(key):
				add(line[i:j], styles.ToneText)
			default:
				add(line[i:j], styles.ToneBody)
			}
			i = j
		case strings.IndexByte("{}[],:", ch) >= 0:
			switch ch {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			add(string(ch), styles.ToneFaint)
			i++
		default:
			j := i + 1
			for j < len(line) && strings.IndexByte(`"{}[],:`, line[j]) < 0 {
				j++
			}
			add(line[i:j], styles.ToneBody)
			i = j
		}
	}
	return c
}

// tintLogfmt tints a line of key=value pairs. Like loki.Fields, it takes a line for logfmt when at
// least two words are pairs and they make up most of it.
func tintLogfmt(line string) (cell, bool) {
	var c cell
	words, pairs := 0, 0
	last := 0 // what is not yet in c
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
		if i >= len(line) || line[i] != '=' || i == start {
			for i < len(line) && line[i] != ' ' {
				if line[i] == '"' {
					i = skipQuoted(line, i)
					continue
				}
				i++
			}
			continue
		}
		pairs++
		key := line[start:i]
		if start > last {
			c = append(c, styles.Span{Text: line[last:start], Tone: styles.ToneBody})
		}
		c = append(c, styles.Span{Text: line[start : i+1], Tone: styles.ToneFaint})
		i++ // =
		vstart := i
		if i < len(line) && line[i] == '"' {
			i = skipQuoted(line, i)
		} else {
			for i < len(line) && line[i] != ' ' {
				i++
			}
		}
		tone := styles.ToneBody
		if isMessageKey(key) {
			tone = styles.ToneText
		}
		if i > vstart {
			c = append(c, styles.Span{Text: line[vstart:i], Tone: tone})
		}
		last = i
	}
	if pairs < 2 || pairs*3 < words*2 {
		return nil, false
	}
	if last < len(line) {
		c = append(c, styles.Span{Text: line[last:], Tone: styles.ToneBody})
	}
	return c, true
}

// skipQuoted returns where the quoted string starting at i ends.
func skipQuoted(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(s)
}

// leadingTimestamp is the length of a date and time a line starts with, like
// "2026-10-01 12:00:00.123" or "2026-10-01T12:00:00Z", with the space after it, or 0.
func leadingTimestamp(line string) int {
	const layout = "0000-00-00?00:00:00"
	if len(line) < len(layout) {
		return 0
	}
	for i := 0; i < len(layout); i++ {
		c := line[i]
		switch layout[i] {
		case '0':
			if c < '0' || c > '9' {
				return 0
			}
		case '?':
			if c != ' ' && c != 'T' {
				return 0
			}
		default:
			if c != layout[i] {
				return 0
			}
		}
	}
	n := len(layout)
	for n < len(line) && strings.IndexByte("0123456789.,:+-Z", line[n]) >= 0 {
		n++
	}
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

// mark sets the byte ranges of the text of c apart, in the warm ink and bold, as the filter
// highlights its matches.
func mark(c cell, ranges [][]int) cell {
	if len(ranges) == 0 {
		return c
	}
	out := make(cell, 0, len(c)+2*len(ranges))
	pos, r := 0, 0
	for _, s := range c {
		end := pos + len(s.Text)
		for at := pos; at < end; {
			for r < len(ranges) && ranges[r][1] <= at {
				r++
			}
			cut, marked := end, false
			if r < len(ranges) {
				switch {
				case ranges[r][0] <= at:
					cut, marked = min(end, ranges[r][1]), true
				case ranges[r][0] < end:
					cut = ranges[r][0]
				}
			}
			span := styles.Span{Text: s.Text[at-pos : cut-pos], Tone: s.Tone, Bold: s.Bold}
			if marked {
				span.Tone, span.Bold = styles.ToneWarm, true
			}
			out = append(out, span)
			at = cut
		}
		pos = end
	}
	return out
}

// cut splits c at the byte offsets, which must be ascending, into len(offsets)+1 cells.
func cut(c cell, offsets []int) []cell {
	out := make([]cell, 0, len(offsets)+1)
	var cur cell
	pos, o := 0, 0
	for _, s := range c {
		text := s.Text
		for o < len(offsets) && offsets[o] < pos+len(text) {
			n := offsets[o] - pos
			if n > 0 {
				cur = append(cur, styles.Span{Text: text[:n], Tone: s.Tone, Bold: s.Bold})
			}
			out = append(out, cur)
			cur = nil
			text = text[max(n, 0):]
			pos += max(n, 0)
			o++
		}
		if text != "" {
			cur = append(cur, styles.Span{Text: text, Tone: s.Tone, Bold: s.Bold})
		}
		pos += len(text)
	}
	for ; o < len(offsets); o++ {
		out = append(out, cur)
		cur = nil
	}
	return append(out, cur)
}
