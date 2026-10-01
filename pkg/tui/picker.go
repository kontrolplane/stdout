package tui

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kontrolplane/stdout/pkg/loki"
	"github.com/kontrolplane/stdout/pkg/tui/commands"
	"github.com/kontrolplane/stdout/pkg/tui/messages"
	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

const (
	focusLabels = iota
	focusValues
)

// labelValues is what is known of the values of a label in one scope.
type labelValues struct {
	values  []string
	volumes map[string]uint64
	loading bool
	err     error
}

type pickerState struct {
	loaded      bool
	labels      []string
	labelsTable dataTable
	valuesTable dataTable
	focus       int
	values      map[string]*labelValues // by valuesKey
	noVolume    bool                    // the server does not report volumes
	valuesGen   uint64
	selection   map[string]loki.Matcher
	order       []string // the labels with a selection, in the order they were picked

	filtering   bool
	filterInput textinput.Model
	filters     [2]string // of the labels and of the values
}

var (
	labelColumns = []column{
		{title: "label", width: 12, grow: 2},
		{title: "selected", width: 8, grow: 3, drop: 2},
		{title: "values", width: 7, right: true, drop: 1},
	}
	valueColumns = []column{
		{title: "", width: 1},
		{title: "value", width: 12, grow: 1},
		{title: "volume", width: 9, right: true, drop: 2},
		{title: "", width: 8, drop: 1},
	}
)

func newPickerState() pickerState {
	p := pickerState{
		labelsTable: newDataTable(labelColumns, leftContentWidth, pickerTableHeight()),
		valuesTable: newDataTable(valueColumns, rightContentWidth, pickerTableHeight()),
		values:      map[string]*labelValues{},
		selection:   map[string]loki.Matcher{},
		filterInput: initFilterInput("filter…"),
	}
	p.labelsTable.empty = "no labels in this range"
	p.valuesTable.empty = "no values"
	p.valuesTable.Blur()
	return p
}

// pickerTableHeight leaves room for the query, the section headers and the column titles.
func pickerTableHeight() int { return contentHeight - 4 - tableHeaderRows }

func valuesKey(label, scope string) string { return label + "\x00" + scope }

// matchers lists the selection in the order the labels were picked.
func (p pickerState) matchers(except string) []loki.Matcher {
	out := make([]loki.Matcher, 0, len(p.order))
	for _, label := range p.order {
		if label != except {
			out = append(out, p.selection[label])
		}
	}
	return out
}

// query is the stream selector the selection makes.
func (p pickerState) query() string { return loki.Selector(p.matchers("")...) }

// scope is the selector the values of label are listed for: the selection of the other labels,
// when it selects streams by itself. Otherwise all values are listed, as Loki refuses selectors
// that only exclude.
func (p pickerState) scope(label string) string {
	others := p.matchers(label)
	if !loki.Selects(others...) {
		return ""
	}
	return loki.Selector(others...)
}

func (p pickerState) filteredLabels() []string {
	if p.filters[focusLabels] == "" {
		return p.labels
	}
	f := strings.ToLower(p.filters[focusLabels])
	var out []string
	for _, l := range p.labels {
		if strings.Contains(strings.ToLower(l), f) {
			out = append(out, l)
		}
	}
	return out
}

// label is the label under the cursor.
func (p pickerState) label() string {
	labels := p.filteredLabels()
	if c := p.labelsTable.Cursor(); c >= 0 && c < len(labels) {
		return labels[c]
	}
	return ""
}

// current is what is known of the values of the label under the cursor.
func (p pickerState) current() *labelValues {
	label := p.label()
	if label == "" {
		return nil
	}
	return p.values[valuesKey(label, p.scope(label))]
}

// filteredValues lists the values of the label under the cursor that pass the filter, the ones
// that ingested the most first when volumes are known.
func (p pickerState) filteredValues() []string {
	lv := p.current()
	if lv == nil {
		return nil
	}
	f := strings.ToLower(p.filters[focusValues])
	var out []string
	for _, v := range lv.values {
		if f == "" || strings.Contains(strings.ToLower(v), f) {
			out = append(out, v)
		}
	}
	if len(lv.volumes) > 0 {
		slices.SortStableFunc(out, func(a, b string) int { return cmp.Compare(lv.volumes[b], lv.volumes[a]) })
	}
	return out
}

func (p pickerState) updateTables() pickerState {
	labels := p.filteredLabels()
	rows := make([]tableRow, len(labels))
	for i, label := range labels {
		m, selected := p.selection[label]
		name := text(label, styles.ToneText)
		if selected {
			name = cell{styles.B(label, styles.ToneAccent)}
		}
		count := cell{}
		if lv, ok := p.values[valuesKey(label, p.scope(label))]; ok {
			switch {
			case lv.loading:
				count = text("…", styles.ToneFaint)
			case lv.err == nil:
				count = text(formatCount(uint64(len(lv.values))), styles.ToneMuted)
			}
		}
		rows[i] = tableRow{name, matcherCell(m, selected), count}
	}
	p.labelsTable.SetRows(rows)

	values := p.filteredValues()
	lv := p.current()
	var largest uint64
	if lv != nil {
		for _, v := range lv.volumes {
			largest = max(largest, v)
		}
	}
	m := p.selection[p.label()]
	rows = make([]tableRow, len(values))
	for i, v := range values {
		mark := cell{}
		tone := styles.ToneBody
		if slices.Contains(m.Values, v) {
			if m.Exclude {
				mark, tone = text("✗", styles.ToneDanger), styles.ToneDanger
			} else {
				mark, tone = text("✓", styles.ToneAccent), styles.ToneAccent
			}
		}
		display := v
		if v == "" {
			display, tone = "(empty)", styles.ToneFaint
		}
		volume, bar := cell{}, cell{}
		if lv != nil && len(lv.volumes) > 0 {
			b := lv.volumes[v]
			volume = text(formatBytes(b), styles.ToneMuted)
			if largest > 0 {
				bar = styles.Bar(float64(b)/float64(largest), 8, styles.ToneAccent)
			}
		}
		rows[i] = tableRow{mark, text(display, tone), volume, bar}
	}
	p.valuesTable.SetRows(rows)
	switch {
	case lv == nil || lv.loading:
		p.valuesTable.empty = "loading values…"
	case lv.err != nil:
		p.valuesTable.empty = "could not load values: " + loki.Describe(lv.err)
	case len(lv.values) > 0:
		p.valuesTable.empty = "no values match the filter"
	default:
		p.valuesTable.empty = "no values in this range"
	}
	return p
}

// matcherCell sums up the selection of a label: = api, web or ≠ api.
func matcherCell(m loki.Matcher, selected bool) cell {
	if !selected || len(m.Values) == 0 {
		return nil
	}
	op, tone := "= ", styles.ToneAccent
	if m.Exclude {
		op, tone = "≠ ", styles.ToneDanger
	}
	values := slices.Sorted(slices.Values(m.Values))
	return cell{styles.B(op, tone), styles.S(strings.Join(values, ", "), styles.ToneBody)}
}

// toggle adds the value to the selection of label, or takes it out.
func (p pickerState) toggle(label, value string) pickerState {
	m, ok := p.selection[label]
	if !ok {
		m = loki.Matcher{Label: label}
		p.order = append(p.order, label)
	}
	if i := slices.Index(m.Values, value); i >= 0 {
		m.Values = slices.Delete(slices.Clone(m.Values), i, i+1)
	} else {
		m.Values = append(slices.Clone(m.Values), value)
	}
	if len(m.Values) == 0 {
		return p.clearLabel(label)
	}
	p.selection[label] = m
	return p
}

func (p pickerState) clearLabel(label string) pickerState {
	delete(p.selection, label)
	p.order = slices.DeleteFunc(slices.Clone(p.order), func(l string) bool { return l == label })
	return p
}

func (p pickerState) setFocus(focus int) pickerState {
	p.focus = focus
	if focus == focusLabels {
		p.labelsTable.Focus()
		p.valuesTable.Blur()
	} else {
		p.labelsTable.Blur()
		p.valuesTable.Focus()
	}
	return p
}

// valuesCmd loads the values of the label under the cursor once it rests there, unless they are
// known already.
func (m model) valuesCmd() (model, tea.Cmd) {
	label := m.picker.label()
	if label == "" {
		return m, nil
	}
	if _, ok := m.picker.values[valuesKey(label, m.picker.scope(label))]; ok {
		return m, nil
	}
	m.picker.valuesGen++
	return m, commands.ScheduleValues(m.picker.valuesGen)
}

func (m model) onValuesTick(msg messages.ValuesTickMsg) (model, tea.Cmd) {
	if msg.Gen != m.picker.valuesGen || m.page != labelPicker {
		return m, nil
	}
	label := m.picker.label()
	if label == "" {
		return m, nil
	}
	scope := m.picker.scope(label)
	k := valuesKey(label, scope)
	if _, ok := m.picker.values[k]; ok {
		return m, nil
	}
	m.picker.values[k] = &labelValues{loading: true}
	m.picker = m.picker.updateTables()
	return m, commands.LoadValues(m.context, m.client, label, scope, m.config.Since, !m.picker.noVolume)
}

func (m model) onValuesLoaded(msg messages.ValuesLoadedMsg) (model, tea.Cmd) {
	lv := &labelValues{values: msg.Values, err: msg.Err}
	switch {
	case msg.VolumeErr == nil:
		lv.volumes = msg.Volumes
	case loki.IsStatus(msg.VolumeErr, http.StatusNotFound, http.StatusBadRequest, http.StatusNotImplemented, http.StatusMethodNotAllowed):
		// Volumes need Loki 3 with volume_enabled; without them the values are listed alone.
		m.picker.noVolume = true
	}
	m.picker.values[valuesKey(msg.Label, msg.Scope)] = lv
	m.picker = m.picker.updateTables()
	return m, nil
}

func (m model) onLabelsLoaded(msg messages.LabelsLoadedMsg) (model, tea.Cmd) {
	if m.page == labelPicker {
		m.loading = false
	}
	if msg.Err != nil {
		if !m.picker.loaded {
			m.error = "could not load labels: " + loki.Describe(msg.Err)
			return m, nil
		}
		return m.setStatus("could not load labels: "+loki.Describe(msg.Err), styles.ToneDanger)
	}
	prev := m.picker.label()
	m.picker.labels = msg.Labels
	m.picker.loaded = true
	if i := slices.Index(m.picker.filteredLabels(), prev); i >= 0 {
		m.picker.labelsTable.SetCursor(i)
	}
	m.picker = m.picker.updateTables()
	return m.valuesCmd()
}

// tailSelection starts tailing the selection. Without one, the value under the cursor is picked.
func (m model) tailSelection() (model, tea.Cmd) {
	p := m.picker
	if len(p.selection) == 0 && p.focus == focusValues {
		values := p.filteredValues()
		if c := p.valuesTable.Cursor(); c >= 0 && c < len(values) {
			p = p.toggle(p.label(), values[c])
		}
	}
	m.picker = p.updateTables()
	if !loki.Selects(p.matchers("")...) {
		if len(p.selection) == 0 {
			return m.setStatus("pick a value with space first", styles.ToneWarning)
		}
		return m.setStatus("include at least one value, Loki refuses a query that only excludes", styles.ToneWarning)
	}
	return m.startTail(p.query())
}

func (m model) PickerUpdate(msg tea.Msg) (model, tea.Cmd) {
	p := &m.picker
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	if p.filtering {
		switch keyMsg.String() {
		case "enter":
			p.filtering = false
			p.filterInput.Blur()
			return m, nil
		case "esc":
			p.filtering = false
			p.filterInput.Blur()
			p.filters[p.focus] = ""
			m.picker = p.updateTables()
			return m.valuesCmd()
		}
		var cmd tea.Cmd
		p.filterInput, cmd = p.filterInput.Update(msg)
		p.filters[p.focus] = p.filterInput.Value()
		if p.focus == focusLabels {
			p.labelsTable.SetCursor(0)
		} else {
			p.valuesTable.SetCursor(0)
		}
		m.picker = p.updateTables()
		m, values := m.valuesCmd()
		return m, tea.Batch(cmd, values)
	}

	switch {
	case key.Matches(keyMsg, m.keys.Quit):
		m = m.stopTail()
		return m, tea.Quit
	case key.Matches(keyMsg, m.keys.Back):
		switch {
		case p.filters[p.focus] != "":
			p.filters[p.focus] = ""
			p.filterInput.SetValue("")
			m.picker = p.updateTables()
			return m.valuesCmd()
		case p.focus == focusValues:
			m.picker = p.setFocus(focusLabels)
		}
		return m, nil
	case key.Matches(keyMsg, m.keys.SwitchFocus, m.keys.Left, m.keys.Right):
		focus := focusValues
		switch {
		case key.Matches(keyMsg, m.keys.Left):
			focus = focusLabels
		case key.Matches(keyMsg, m.keys.SwitchFocus) && p.focus == focusValues:
			focus = focusLabels
		}
		m.picker = p.setFocus(focus)
		return m, nil
	case key.Matches(keyMsg, m.keys.View):
		if p.focus == focusLabels {
			m.picker = p.setFocus(focusValues)
			return m, nil
		}
		return m.tailSelection()
	case key.Matches(keyMsg, m.keys.Select):
		if p.focus != focusValues {
			return m, nil
		}
		values := p.filteredValues()
		c := p.valuesTable.Cursor()
		if c < 0 || c >= len(values) {
			return m, nil
		}
		m.picker = p.toggle(p.label(), values[c]).updateTables()
		m.picker.valuesTable.SetCursor(slices.Index(m.picker.filteredValues(), values[c]))
		return m, nil
	case key.Matches(keyMsg, m.keys.Negate):
		label := p.label()
		if sel, ok := p.selection[label]; ok {
			sel.Exclude = !sel.Exclude
			p.selection[label] = sel
			m.picker = p.updateTables()
		}
		return m, nil
	case key.Matches(keyMsg, m.keys.Clear):
		m.picker = p.clearLabel(p.label()).updateTables()
		return m.valuesCmd()
	case key.Matches(keyMsg, m.keys.ClearAll):
		p.selection = map[string]loki.Matcher{}
		p.order = nil
		m.picker = p.updateTables()
		return m.valuesCmd()
	case key.Matches(keyMsg, m.keys.Edit):
		return m.openEditor(cmp.Or(p.query(), m.tail.query, "{}"))
	case key.Matches(keyMsg, m.keys.Refresh):
		p.values = map[string]*labelValues{}
		m.picker = p.updateTables()
		m.loading = true
		m.loadingMsg = "loading labels…"
		return m, tea.Batch(commands.LoadLabels(m.context, m.client, m.config.Since), m.spinner.Tick)
	case key.Matches(keyMsg, m.keys.Filter):
		p.filtering = true
		p.filterInput.SetValue(p.filters[p.focus])
		p.filterInput.CursorEnd()
		return m, p.filterInput.Focus()
	}

	if p.focus == focusLabels {
		before := p.labelsTable.Cursor()
		p.labelsTable = p.labelsTable.Update(keyMsg)
		if p.labelsTable.Cursor() != before {
			p.valuesTable.SetCursor(0)
			p.filters[focusValues] = ""
			m.picker = p.updateTables()
			return m.valuesCmd()
		}
		return m, nil
	}
	p.valuesTable = p.valuesTable.Update(keyMsg)
	return m, nil
}

func (m model) PickerView() string {
	p := m.picker
	query := p.query()
	var line string
	if query == "" {
		line = styles.Faint("pick values with space to build a query, enter tails it, e writes one by hand")
	} else {
		line = styles.Faint("query  ") + styles.Fg(styles.ToneText).Render(truncate(styles.Clean(query), contentWidth-11))
	}

	labelsMeta := styles.Faint(formatCount(uint64(len(p.labels))))
	if f := p.filters[focusLabels]; f != "" {
		labelsMeta = styles.Faint(fmt.Sprintf("%d of %d", len(p.filteredLabels()), len(p.labels)))
	}
	labelsTitle := styles.Fg(styles.ToneText).Bold(true)
	valuesTitle := labelsTitle
	if p.focus == focusLabels {
		labelsTitle = styles.Fg(styles.ToneAccent).Bold(true)
	} else {
		valuesTitle = styles.Fg(styles.ToneAccent).Bold(true)
	}
	valuesHeading := "values"
	if label := p.label(); label != "" {
		valuesHeading = "values of " + truncate(styles.Clean(label), rightContentWidth/2)
	}
	var valuesMeta string
	if lv := p.current(); lv != nil && !lv.loading && lv.err == nil {
		valuesMeta = styles.Faint(formatCount(uint64(len(lv.values))))
		if p.filters[focusValues] != "" {
			valuesMeta = styles.Faint(fmt.Sprintf("%d of %d", len(p.filteredValues()), len(lv.values)))
		}
	}

	h := contentHeight - 2
	left := lipgloss.JoinVertical(lipgloss.Left,
		styles.SectionHeaderWith(labelsTitle.Render("labels"), labelsMeta, leftContentWidth),
		"",
		p.labelsTable.View(),
	)
	right := lipgloss.JoinVertical(lipgloss.Left,
		styles.SectionHeaderWith(valuesTitle.Render(valuesHeading), valuesMeta, rightContentWidth),
		"",
		p.valuesTable.View(),
	)
	panel := func(width int) lipgloss.Style {
		return lipgloss.NewStyle().PaddingLeft(2).PaddingRight(2).Width(width).Height(h)
	}
	panels := lipgloss.JoinHorizontal(lipgloss.Top,
		panel(leftPanelWidth).Render(left),
		verticalDivider(h),
		panel(rightPanelWidth).Render(right),
	)
	return lipgloss.JoinVertical(lipgloss.Left, "  "+line, "", panels)
}
