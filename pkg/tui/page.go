package tui

type page uint

const (
	labelPicker page = iota
	tailView
	lineDetails
)

// SwitchPage opens page.
func (m model) SwitchPage(page page) model {
	m.page = page
	m.loading = false
	return m
}
