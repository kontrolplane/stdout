package tui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/stdout/pkg/loki"
	"github.com/kontrolplane/stdout/pkg/tui/commands"
	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

type detailsState struct {
	row      row
	format   string         // json, logfmt or text
	viewport viewport.Model // the message
	fields   viewport.Model // the time, labels and fields, which can be more than fit
	onFields bool           // the keys scroll the fields rather than the message
}

func detailsViewportHeight() int { return contentHeight - 2 } // the section header and the gap under it

// openDetails shows the row at i of the tail's view. The tail stops following, so the cursor is
// still on the line once the details are left.
func (m model) openDetails(i int) (model, tea.Cmd) {
	if i < 0 || i >= len(m.tail.view) {
		return m, nil
	}
	m.tail.cursor = i
	m.tail.follow = false
	m.tail = m.tail.scroll()
	d := &m.details
	d.row = m.tail.visible(i)
	d.format = loki.Format(d.row.entry.Line)
	d.viewport = viewport.New(viewport.WithWidth(rightContentWidth), viewport.WithHeight(detailsViewportHeight()))
	d.viewport.SetContent(renderLine(d.row.entry.Line, rightContentWidth))
	d.fields = viewport.New(viewport.WithWidth(leftContentWidth), viewport.WithHeight(contentHeight))
	d.fields.SetContent(m.detailsFields())
	if !d.fieldsOverflow() {
		d.onFields = false
	}
	return m.SwitchPage(lineDetails), nil
}

// resize wraps the line again at the current width, keeping the scroll positions.
func (m *model) resizeDetails() {
	d := &m.details
	offset := d.viewport.YOffset()
	d.viewport.SetWidth(rightContentWidth)
	d.viewport.SetHeight(detailsViewportHeight())
	d.viewport.SetContent(renderLine(d.row.entry.Line, rightContentWidth))
	d.viewport.SetYOffset(offset)
	offset = d.fields.YOffset()
	d.fields.SetWidth(leftContentWidth)
	d.fields.SetHeight(contentHeight)
	d.fields.SetContent(m.detailsFields())
	d.fields.SetYOffset(offset)
	if !d.fieldsOverflow() {
		d.onFields = false
	}
}

// fieldsOverflow reports whether the fields are more than the panel shows, so it scrolls.
func (d detailsState) fieldsOverflow() bool {
	return d.fields.TotalLineCount() > d.fields.Height()
}

// renderLine formats a line for reading: JSON indented and coloured, anything else wrapped.
func renderLine(line string, width int) string {
	if line == "" {
		return styles.Faint("empty line")
	}
	text, isJSON := loki.Pretty(line)
	text = ansi.Hardwrap(styles.CleanBlock(text), width, true)
	if isJSON {
		return highlightJSON(text)
	}
	var b strings.Builder
	paintLines(&b, styles.ToneBody, text)
	return b.String()
}

func (m model) DetailsUpdate(msg tea.Msg) (model, tea.Cmd) {
	d := &m.details
	// The fields hold the age and whether the line is pinned, which change while it is open.
	d.fields.SetContent(m.detailsFields())
	scrolled := &d.viewport
	if d.onFields {
		scrolled = &d.fields
	}
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(keyMsg, m.keys.SwitchFocus):
			d.onFields = !d.onFields && d.fieldsOverflow()
			return m, nil
		case key.Matches(keyMsg, m.keys.Quit, m.keys.Back):
			m = m.SwitchPage(tailView)
			m.tail = m.tail.scroll()
			return m, nil
		case key.Matches(keyMsg, m.keys.CopyToClipboard):
			return m, commands.CopyToClipboard(d.row.entry.Line)
		case key.Matches(keyMsg, m.keys.Select):
			if m.tail.pins[d.row.id] {
				delete(m.tail.pins, d.row.id)
			} else {
				m.tail.pins[d.row.id] = true
			}
			if m.tail.pinnedOnly {
				m.tail = m.tail.rebuildView()
			}
			return m, nil
		case key.Matches(keyMsg, m.keys.Previous, m.keys.Next):
			i := m.tail.find(d.row)
			if i >= len(m.tail.view) || m.tail.visible(i).id != d.row.id {
				return m.setStatus("the line is no longer in the tail", styles.ToneWarning)
			}
			if key.Matches(keyMsg, m.keys.Previous) {
				i--
			} else {
				i++
			}
			if i < 0 || i >= len(m.tail.view) {
				return m, nil
			}
			return m.openDetails(i)
		case key.Matches(keyMsg, m.keys.Stream):
			return m.startTail(loki.StreamSelector(d.row.entry.Labels))
		case key.Matches(keyMsg, m.keys.Top):
			scrolled.GotoTop()
			return m, nil
		case key.Matches(keyMsg, m.keys.Bottom):
			scrolled.GotoBottom()
			return m, nil
		}
	}
	var cmd tea.Cmd
	*scrolled, cmd = scrolled.Update(msg)
	return m, cmd
}

// detailsFields renders the left panel: the time and level of the line, its labels, structured
// metadata and parsed labels, and the fields of a structured line.
func (m model) detailsFields() string {
	d := m.details
	e := d.row.entry

	level := styles.S("unknown", styles.ToneFaint)
	if d.row.level != loki.LevelUnknown {
		level = styles.B(d.row.level.String(), levelTone(d.row.level))
	}
	pinned := styles.S("no, space pins it", styles.ToneFaint)
	if m.tail.pins[d.row.id] {
		pinned = styles.S("yes", styles.ToneAccent)
	}
	title := styles.Fg(styles.ToneText).Bold(true)
	if d.onFields {
		title = styles.Fg(styles.ToneAccent).Bold(true)
	}
	meta := ""
	if d.fieldsOverflow() {
		meta = styles.Faint("tab scrolls")
		if d.onFields {
			meta = styles.Faint(fmt.Sprintf("%d%%", int(d.fields.ScrollPercent()*100)))
		}
	}
	left := []string{
		styles.SectionHeaderWith(title.Render("line"), meta, leftContentWidth),
		panelRowSpans("time", styles.S(e.Time.Local().Format(timeFormat), styles.ToneBody)),
		panelRowSpans("age", styles.S(formatAgo(e.Time), styles.ToneBody)),
		panelRowSpans("level", level),
		panelRowSpans("size", styles.S(formatBytes(uint64(len(e.Line))), styles.ToneBody), styles.S("  "+d.format, styles.ToneFaint)),
		panelRowSpans("pinned", pinned),
	}

	sections := []struct {
		title  string
		labels loki.Labels
	}{
		{"labels", e.Labels},
		{"structured metadata", e.Metadata},
		{"parsed", e.Parsed},
	}
	for _, s := range sections {
		if len(s.labels) == 0 && s.title != "labels" {
			continue
		}
		left = append(left, "", styles.SectionHeaderWith(styles.Bold(s.title), styles.Faint(fmt.Sprint(len(s.labels))), leftContentWidth))
		names := slices.Sorted(maps.Keys(s.labels))
		for _, name := range names {
			left = append(left, fieldRow(name, s.labels[name]))
		}
	}

	if fields := loki.Fields(e.Line); len(fields) > 0 {
		left = append(left, "", styles.SectionHeaderWith(styles.Bold("fields"), styles.Faint(fmt.Sprint(len(fields))), leftContentWidth))
		for _, f := range fields {
			left = append(left, fieldRow(f.Key, f.Value))
		}
	}
	return strings.Join(left, "\n")
}

func (m model) DetailsView() string {
	d := m.details
	fields := d.fields
	fields.SetContent(m.detailsFields())

	vp := d.viewport
	meta := d.format
	if vp.TotalLineCount() > vp.Height() {
		meta += fmt.Sprintf(" · %d%%", int(vp.ScrollPercent()*100))
	}
	title := styles.Fg(styles.ToneText).Bold(true)
	if !d.onFields && d.fieldsOverflow() {
		title = styles.Fg(styles.ToneAccent).Bold(true)
	}
	right := lipgloss.JoinVertical(lipgloss.Left,
		styles.SectionHeaderWith(title.Render("message"), styles.Faint(meta), rightContentWidth),
		"",
		vp.View(),
	)
	return splitPanels(fields.View(), right)
}

// fieldRow renders a label or field. Names longer than the label column take what they need, up to
// half the panel, so names that share a prefix stay apart.
func fieldRow(name, value string) string {
	name = styles.Clean(name)
	labelWidth := min(max(panelLabelWidth-2, ansi.StringWidth(name)), leftContentWidth/2)
	label := styles.Fg(styles.ToneFaint).Width(labelWidth).Align(lipgloss.Right).Render(truncate(name, labelWidth))
	return label + "  " + renderCell(cell{styles.S(value, styles.ToneBody)}, column{width: leftContentWidth - labelWidth - 2}, nil)
}
