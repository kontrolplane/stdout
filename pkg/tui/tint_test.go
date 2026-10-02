package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

// spans renders a cell as text|tone pairs, with bold spans starred, to compare in tests.
func spans(c cell) string {
	parts := make([]string, len(c))
	for i, s := range c {
		star := ""
		if s.Bold {
			star = "*"
		}
		parts[i] = fmt.Sprintf("%s%q:%d", star, s.Text, s.Tone)
	}
	return strings.Join(parts, " ")
}

func joined(c cell) string {
	var b strings.Builder
	for _, s := range c {
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestTint(t *testing.T) {
	F, B, T := styles.ToneFaint, styles.ToneBody, styles.ToneText
	tests := []struct {
		line string
		want cell
	}{
		{`{"level":"info","msg":"hi \"there\"","n":3,"o":{"msg":"x"}}`, cell{
			{Text: `{"level":`, Tone: F}, {Text: `"info"`, Tone: B}, {Text: `,"msg":`, Tone: F},
			{Text: `"hi \"there\""`, Tone: T}, {Text: `,"n":`, Tone: F}, {Text: `3`, Tone: B},
			{Text: `,"o":{"msg":`, Tone: F}, {Text: `"x"`, Tone: B}, {Text: `}}`, Tone: F},
		}},
		{`level=info msg="request done" path=/api status=200`, cell{
			{Text: `level=`, Tone: F}, {Text: `info`, Tone: B}, {Text: ` `, Tone: B}, {Text: `msg=`, Tone: F},
			{Text: `"request done"`, Tone: T}, {Text: ` `, Tone: B}, {Text: `path=`, Tone: F}, {Text: `/api`, Tone: B},
			{Text: ` `, Tone: B}, {Text: `status=`, Tone: F}, {Text: `200`, Tone: B},
		}},
		{`2026-10-01 21:24:45.123 [DEBUG] job done`, cell{
			{Text: `2026-10-01 21:24:45.123 `, Tone: F}, {Text: `[DEBUG] job done`, Tone: B},
		}},
		{`the answer is x=42 according to the docs`, cell{{Text: `the answer is x=42 according to the docs`, Tone: B}}},
		{`{"broken":`, cell{{Text: `{"broken":`, Tone: F}}},
	}
	for _, tt := range tests {
		got := tint(tt.line)
		if spans(got) != spans(tt.want) {
			t.Errorf("tint(%s)\n got %s\nwant %s", tt.line, spans(got), spans(tt.want))
		}
		if joined(got) != tt.line {
			t.Errorf("tint(%s) changed the text to %s", tt.line, joined(got))
		}
	}
}

func TestMarkAndCut(t *testing.T) {
	c := tint(`a=1 msg=timeout b=2`)
	marked := mark(c, [][]int{{2, 3}, {8, 12}, {13, 15}})
	if joined(marked) != `a=1 msg=timeout b=2` {
		t.Fatalf("mark changed the text: %s", spans(marked))
	}
	var hot []string
	for _, s := range marked {
		if s.Bold && s.Tone == styles.ToneWarm {
			hot = append(hot, s.Text)
		}
	}
	if got := strings.Join(hot, "|"); got != "1|time|ut" {
		t.Errorf("marked %q, in %s", got, spans(marked))
	}

	pieces := cut(marked, []int{5, 10})
	var texts []string
	for _, p := range pieces {
		texts = append(texts, joined(p))
	}
	if got := strings.Join(texts, "|"); got != "a=1 m|sg=ti|meout b=2" {
		t.Errorf("cut = %q", got)
	}
	if got := len(cut(cell{{Text: "abc"}}, []int{3})); got != 2 {
		t.Errorf("a cut at the end makes %d pieces", got)
	}
}

func TestWrapLineKeepsWordsWhole(t *testing.T) {
	line := `{"customer":"cus_4419","duration_ms":113,"msg":"request served"} and a verylongwordthatdoesnotfitanywhere`
	pieces := wrapLine(line, 24)
	if strings.Join(pieces, "") != line {
		t.Fatalf("pieces %q lost text", pieces)
	}
	want := []string{`{"customer":"cus_4419",`, `"duration_ms":113,`, `"msg":"request served"} `,
		// No break in the second half: cut where the piece is full rather than leave it short.
		`and a verylongwordthatdo`, `esnotfitanywhere`}
	if strings.Join(pieces, "|") != strings.Join(want, "|") {
		t.Errorf("pieces\n%q\nwant\n%q", pieces, want)
	}
	for _, p := range wrapLine("日本語のログ日本語のログ", 5) {
		if w := textWidth(p); w > 5 {
			t.Errorf("piece %q is %d wide", p, w)
		}
	}
}
