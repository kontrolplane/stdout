package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/stdout/pkg/loki"
	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

const (
	headerIndent  = 1
	headerGap     = 2 // the least room between the fact columns
	maxCrumbWidth = 60
)

// headerFact is one label and value in the header grid.
type headerFact struct {
	label string
	value cell
}

// factsWidth is the width facts need to show in full.
func factsWidth(facts []headerFact) int {
	valueWidth := 0
	for _, f := range facts {
		w := 0
		for _, s := range f.value {
			w += ansi.StringWidth(s.Text)
		}
		valueWidth = max(valueWidth, w)
	}
	return factsLabelWidth(facts) + 2 + valueWidth
}

func factsLabelWidth(facts []headerFact) int {
	w := 0
	for _, f := range facts {
		w = max(w, len(f.label))
	}
	return w
}

// factColumn renders facts as rows with their labels aligned, filling width.
func factColumn(width int, facts ...headerFact) string {
	labelWidth := factsLabelWidth(facts)
	lines := make([]string, len(facts))
	for i, f := range facts {
		label := styles.Faint(f.label + strings.Repeat(" ", labelWidth-len(f.label)+2))
		lines[i] = label + renderCell(f.value, column{width: max(1, width-labelWidth-2)}, nil)
	}
	return strings.Join(lines, "\n")
}

// renderHeader draws a grid of connection, server and tail facts.
func (m model) renderHeader() string {
	width := frameWidth - headerIndent
	columns := [][]headerFact{m.connectionFacts(), m.serverFacts()}
	columns = append(columns, m.pageFacts()...)
	facts := factsGrid(width, columns...)
	if facts == "" {
		facts = factsGrid(width, columns[1:]...)
	}
	if facts == "" {
		facts = factsGrid(-1, columns[1:]...)
	}
	return lipgloss.NewStyle().MarginLeft(headerIndent).Render(facts)
}

// factsGrid lays the fact columns out across width, or returns "" when they do not fit. Spare
// room goes between the columns so the grid spans the header. Without any, the first column gives
// up width, since an address reads fine cut short while the numbers do not, but never below a
// floor that keeps it readable. A negative width lays the columns out whether they fit or not.
func factsGrid(width int, columns ...[]headerFact) string {
	columns = slices.DeleteFunc(columns, func(f []headerFact) bool { return f == nil })
	if len(columns) == 0 {
		return ""
	}
	natural := make([]int, len(columns))
	total := 0
	for i, facts := range columns {
		natural[i] = factsWidth(facts)
		total += natural[i]
	}
	floor := min(natural[0], factsLabelWidth(columns[0])+2+16)

	gaps := max(len(columns)-1, 1)
	spare := width - total
	if short := (len(columns)-1)*headerGap - spare; short > 0 && width >= 0 {
		if short > natural[0]-floor {
			return ""
		}
		natural[0] -= short
		spare = (len(columns) - 1) * headerGap
	}
	spare = max(spare, len(columns)-1)

	rendered := make([]string, 0, 2*len(columns)-1)
	for i, facts := range columns {
		if i > 0 {
			gap := spare / gaps
			if i <= spare%gaps {
				gap++
			}
			rendered = append(rendered, strings.Repeat(" ", gap))
		}
		rendered = append(rendered, factColumn(natural[i], facts...))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

// pendingValue stands in for facts the server has not reported yet.
var pendingValue = text("…", styles.ToneFaint)

func (m model) connectionFacts() []headerFact {
	return []headerFact{
		{"addr", text(orDash(m.info.Addr), styles.ToneBody)},
		{"tenant", text(orDash(m.info.Tenant), styles.ToneBody)},
	}
}

func (m model) serverFacts() []headerFact {
	version := pendingValue
	if m.buildOK {
		version = text(orDash(m.build.Version), styles.ToneBody)
	}
	return []headerFact{{"loki", version}, {"auth", text(m.info.Auth, styles.ToneBody)}}
}

// pageFacts are the facts of what is on screen: the labels of the picker, or the streams and
// lines of the tail.
func (m model) pageFacts() [][]headerFact {
	n := func(v uint64, zero styles.Tone) cell {
		if v == 0 {
			return text("0", zero)
		}
		return text(compactCount(v), styles.ToneBody)
	}
	if m.page == labelPicker {
		labels := pendingValue
		if m.picker.loaded {
			labels = n(uint64(len(m.picker.labels)), styles.ToneFaint)
		}
		return [][]headerFact{{
			{"labels", labels},
			{"range", text("last "+formatSince(m.config.Since), styles.ToneBody)},
		}}
	}
	t := m.tail
	buffered := n(uint64(len(t.rows)), styles.ToneFaint)
	if len(t.rows) > 0 {
		buffered = append(buffered, styles.S(" of "+compactCount(t.total), styles.ToneFaint))
	}
	return [][]headerFact{
		{
			{"streams", n(uint64(len(t.streams)), styles.ToneFaint)},
			{"rate", text(formatRate(t.rate.perSecond(time.Now())), styles.ToneBody)},
		},
		{
			{"lines", buffered},
			{"dropped", n(t.dropped, styles.ToneFaint)},
		},
	}
}

// followState renders the state of the follow for the bottom edge of the frame.
func (m model) followState() string {
	t := m.tail
	var status []styles.Span
	switch {
	case !t.running && t.state == loki.Failed:
		status = []styles.Span{styles.S("● ", styles.ToneDanger), styles.S("stopped", styles.ToneDanger)}
	case !t.running:
		status = []styles.Span{styles.S("○ ", styles.ToneMuted), styles.S("stopped", styles.ToneMuted)}
	case t.state == loki.Connecting:
		status = []styles.Span{styles.S("● ", styles.ToneWarning), styles.S("connecting", styles.ToneWarning)}
	case t.state == loki.Retrying:
		label := "reconnecting"
		if wait := time.Until(t.retryAt).Round(time.Second); wait > 0 {
			label = fmt.Sprintf("reconnecting in %s", wait)
		}
		status = []styles.Span{styles.S("● ", styles.ToneWarning), styles.S(label, styles.ToneWarning)}
	default:
		status = []styles.Span{styles.S("● ", styles.ToneSuccess), styles.S("live", styles.ToneBody)}
	}
	if t.paused {
		held := "⏸ paused"
		if len(t.pending) > 0 {
			held += " · " + compactCount(uint64(len(t.pending))) + " held"
		}
		status = append([]styles.Span{styles.S(held, styles.ToneWarning), styles.S(" · ", styles.ToneFaint)}, status...)
	}
	return styles.Render(status...)
}

// breadcrumb names the page in the top edge of the frame.
func (m model) breadcrumb() []styles.Span {
	var trail []string
	switch m.page {
	case labelPicker:
		trail = []string{"labels"}
	case tailView:
		trail = []string{"tail", m.tail.query}
		if m.tail.pinnedOnly {
			trail = append(trail, "pinned")
		}
	case lineDetails:
		trail = []string{"tail", m.tail.query, m.details.row.entry.Time.Local().Format(clockFormat)}
	}
	var spans []styles.Span
	for i, t := range trail {
		if i > 0 {
			spans = append(spans, styles.S(" › ", styles.ToneFaint))
		}
		if i > 0 && i < len(trail)-1 {
			t = truncate(styles.Clean(t), maxCrumbWidth)
		}
		if i == len(trail)-1 {
			spans = append(spans, styles.B(t, styles.ToneText))
		} else {
			spans = append(spans, styles.S(t, styles.ToneMuted))
		}
	}
	return spans
}
