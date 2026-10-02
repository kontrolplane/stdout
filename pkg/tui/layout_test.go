package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestViewsFit renders every page at every width from the smallest supported up, since a column
// too many wraps the line in the terminal and shifts everything under it.
func TestViewsFit(t *testing.T) {
	defer setLayout(140, 40)
	for w := 82; w <= 240; w += 7 {
		for _, h := range []int{20, 38, 45} {
			for _, m := range []model{pickerModel(t), tailModel(t), send(t, tailModel(t), "enter"), send(t, tailModel(t), "?")} {
				m = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
				lines := strings.Split(m.render(), "\n")
				if len(lines) > h {
					t.Errorf("%dx%d page %v: %d lines", w, h, m.page, len(lines))
				}
				for i, l := range lines {
					if lw := ansi.StringWidth(l); lw > w {
						t.Errorf("%dx%d page %v line %d: %d wide: %q", w, h, m.page, i, lw, ansi.Strip(l))
					}
				}
			}
		}
	}
}

func TestOverlayKeepsThePageAround(t *testing.T) {
	defer setLayout(140, 40)
	setLayout(minContentWidth+chromeWidth, minContentHeight+chromeHeight)
	page := make([]string, contentHeight)
	for i := range page {
		page[i] = strings.Repeat(string(rune('a'+i)), contentWidth)
	}
	got := strings.Split(ansi.Strip(overlay(strings.Join(page, "\n"), "XX")), "\n")
	mid := (contentHeight - 1) / 2
	for i, line := range got {
		want := page[i]
		if i == mid {
			// The card, with a blank cell either side of it.
			at := (contentWidth - 4) / 2
			want = want[:at] + " XX " + want[at+4:]
		}
		if line != want {
			t.Errorf("line %d = %q, want %q", i, line, want)
		}
	}
}

// Over a page with blank lines, as an empty table has, the card still sits in the middle.
func TestOverlayCentresOverBlankLines(t *testing.T) {
	defer setLayout(140, 40)
	setLayout(minContentWidth+chromeWidth, minContentHeight+chromeHeight)
	got := strings.Split(ansi.Strip(overlay("short", "XX")), "\n")
	mid := (contentHeight - 1) / 2
	if at := strings.Index(got[mid], "XX"); at != (contentWidth-4)/2+1 {
		t.Errorf("card at column %d, want %d: %q", at, (contentWidth-4)/2+1, got[mid])
	}
	for i, line := range got {
		if w := ansi.StringWidth(line); w != contentWidth {
			t.Errorf("line %d is %d wide, want the content width %d", i, w, contentWidth)
		}
	}
}
