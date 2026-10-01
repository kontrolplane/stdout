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
