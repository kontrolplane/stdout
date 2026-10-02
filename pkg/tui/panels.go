package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

const panelLabelWidth = 18

var (
	leftPanelWidth    int
	rightPanelWidth   int
	leftContentWidth  int
	rightContentWidth int
	panelValueWidth   int
)

func init() { setPanelLayout() }

func setPanelLayout() {
	leftPanelWidth = (contentWidth - 1) / 2             // Split evenly, 1 for divider
	rightPanelWidth = contentWidth - leftPanelWidth - 1 // Remainder goes to right panel
	leftContentWidth = leftPanelWidth - 4               // Panel padding (4)
	rightContentWidth = rightPanelWidth - 4             // Panel padding (4)
	panelValueWidth = leftContentWidth - panelLabelWidth - 2
}

// panelRowSpans renders a right aligned label next to its value, made of several tones. A value
// too long for the panel is cut short with an ellipsis.
func panelRowSpans(label string, value ...styles.Span) string {
	return panelLabel(label) + renderCell(value, column{width: panelValueWidth}, nil)
}

// panelLabel renders the right aligned label of a panel row.
func panelLabel(label string) string {
	return styles.Fg(styles.ToneFaint).
		Width(panelLabelWidth).
		Align(lipgloss.Right).
		PaddingRight(2).
		Render(truncate(label, panelLabelWidth-2))
}

// splitPanels joins a left and right panel with a vertical divider, filling the content area.
func splitPanels(left, right string) string {
	panel := func(width int) lipgloss.Style {
		return lipgloss.NewStyle().
			PaddingLeft(2).
			PaddingRight(2).
			Width(width).
			Height(contentHeight)
	}

	content := lipgloss.JoinHorizontal(lipgloss.Top,
		panel(leftPanelWidth).Render(left),
		verticalDivider(contentHeight),
		panel(rightPanelWidth).Render(right),
	)
	return lipgloss.PlaceHorizontal(contentWidth, lipgloss.Center, content)
}
