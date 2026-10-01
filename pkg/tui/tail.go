package tui

import (
	"context"
	"fmt"
	"image/color"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/stdout/pkg/loki"
	"github.com/kontrolplane/stdout/pkg/tui/commands"
	"github.com/kontrolplane/stdout/pkg/tui/messages"
	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

// row is one line of the tail.
type row struct {
	id     uint64 // in the order lines arrived, it never changes
	entry  loki.Entry
	level  loki.Level
	stream string // the stream's labels as a selector, a key for streams
}

type streamInfo struct {
	labels loki.Labels
	count  int    // lines of the stream in the buffer
	text   string // the values of the varying labels, as shown next to a line
}

type tailState struct {
	query   string
	gen     uint64 // the follow whose messages are current
	cancel  context.CancelFunc
	events  <-chan loki.FollowEvent
	running bool
	state   loki.FollowState
	err     error
	retryAt time.Time
	started time.Time

	rows    []row // oldest first
	nextID  uint64
	view    []int // the indices of the rows shown, after the filter and the pinned view
	cursor  int   // into view
	offset  int   // the first row of view on screen
	follow  bool  // the cursor stays on the newest line
	paused  bool  // lines are held back rather than shown
	pending []loki.Entry
	limit   int // the most rows kept

	pins       map[uint64]bool
	pinnedOnly bool

	filtering   bool
	filterInput textinput.Model
	filter      lineFilter

	wrap       bool
	hideLabels bool

	streams    map[string]*streamInfo
	varying    []string
	labelWidth int

	rate    rateMeter
	total   uint64 // lines received, also the ones trimmed or filtered out
	dropped uint64 // lines the server dropped because the tail fell behind

	older    bool      // a load of older lines is in flight
	olderEnd time.Time // where the next load of older lines ends
}

func newTailState(limit int) tailState {
	return tailState{
		limit:       limit,
		follow:      true,
		pins:        map[uint64]bool{},
		streams:     map[string]*streamInfo{},
		filterInput: initFilterInput("text, /regex/, or !text to hide lines…"),
	}
}

// lineFilter narrows the tail down to lines that match, or with exclude, that do not.
type lineFilter struct {
	text    string
	re      *regexp.Regexp
	exclude bool
}

// newLineFilter reads a filter: plain text matches case insensitively, /text/ is a regular
// expression, and a leading ! hides the lines that match instead.
func newLineFilter(s string) (lineFilter, error) {
	f := lineFilter{text: s}
	if s == "" {
		return f, nil
	}
	if strings.HasPrefix(s, "!") {
		f.exclude = true
		s = s[1:]
	}
	if s == "" {
		return lineFilter{}, nil
	}
	pattern := "(?i)" + regexp.QuoteMeta(s)
	if len(s) > 2 && strings.HasPrefix(s, "/") && strings.HasSuffix(s, "/") {
		pattern = s[1 : len(s)-1]
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return lineFilter{}, err
	}
	f.re = re
	return f, nil
}

func (f lineFilter) active() bool { return f.re != nil }

func (f lineFilter) match(line string) bool {
	if f.re == nil {
		return true
	}
	return f.re.MatchString(line) != f.exclude
}

const rateWindow = 10 * time.Second

// rateMeter counts lines per second over the last ten whole seconds. It holds one more second,
// the current one, which is still counting.
type rateMeter struct {
	counts [11]int
	secs   [11]int64
}

func (r *rateMeter) add(n int, now time.Time) {
	s := now.Unix()
	i := s % int64(len(r.counts))
	if r.secs[i] > s {
		return // a later second holds the slot, this one is past the window
	}
	if r.secs[i] != s {
		r.secs[i], r.counts[i] = s, 0
	}
	r.counts[i] += n
}

// perSecond is the rate over the last ten whole seconds.
func (r rateMeter) perSecond(now time.Time) float64 {
	s := now.Unix()
	sum := 0
	window := int64(len(r.counts) - 1)
	for i, sec := range r.secs {
		if sec < s && sec >= s-window {
			sum += r.counts[i]
		}
	}
	return float64(sum) / float64(window)
}

// tailBodyHeight is the number of lines the tail shows.
func tailBodyHeight() int { return contentHeight }

const (
	timeWidth  = len(clockFormat)
	levelWidth = 5
	tailGutter = 3 // the cursor, the pin marker and a space
)

// lineWidth is the room left for the line itself, after the time, the level and the labels.
func (t tailState) lineWidth() int {
	w := contentWidth - tailGutter - timeWidth - 1 - levelWidth - 1 - 1
	if lw := t.labelsShown(); lw > 0 {
		w -= lw + 1
	}
	return max(w, 10)
}

// labelsShown is the width of the labels column, 0 when it is hidden.
func (t tailState) labelsShown() int {
	if t.hideLabels || len(t.varying) == 0 {
		return 0
	}
	return t.labelWidth
}

func (t tailState) visible(i int) row { return t.rows[t.view[i]] }

// selected returns the row under the cursor.
func (t tailState) selected() (row, bool) {
	if t.cursor < 0 || t.cursor >= len(t.view) {
		return row{}, false
	}
	return t.visible(t.cursor), true
}

func (t tailState) match(r row) bool {
	if t.pinnedOnly && !t.pins[r.id] {
		return false
	}
	return t.filter.match(r.entry.Line)
}

// add takes in new lines: in time order, with the late ones put in their place. While paused they
// wait in pending.
func (t tailState) add(entries []loki.Entry, now time.Time) tailState {
	if len(entries) == 0 {
		return t
	}
	t.total += uint64(len(entries))
	for _, e := range entries {
		// By the time of the line, so the history the tail starts with does not read as a burst.
		if now.Sub(e.Time) < rateWindow {
			t.rate.add(1, e.Time)
		}
	}
	if t.paused {
		t.pending = append(t.pending, entries...)
		if over := len(t.pending) - t.limit; over > 0 {
			t.pending = slices.Delete(t.pending, 0, over)
		}
		return t
	}
	return t.insert(entries)
}

func (t tailState) insert(entries []loki.Entry) tailState {
	a := t.anchor()
	rebuild := false
	for _, e := range entries {
		r := row{id: t.nextID, entry: e, level: loki.DetectLevel(e), stream: e.Labels.String()}
		t.nextID++
		t.track(r, 1)
		n := len(t.rows)
		if n > 0 && e.Time.Before(t.rows[n-1].entry.Time) {
			i := sort.Search(n, func(i int) bool { return t.rows[i].entry.Time.After(e.Time) })
			t.rows = slices.Insert(t.rows, i, r)
			rebuild = true
			continue
		}
		t.rows = append(t.rows, r)
		if !rebuild && t.match(r) {
			t.view = append(t.view, len(t.rows)-1)
		}
	}
	if rebuild {
		t = t.rebuildViewAt(a)
	}
	t = t.trim()
	t = t.refreshLabels()
	return t.scroll()
}

// prepend puts older lines before the ones in the buffer, leaving out the ones it already holds,
// and reports how many it added.
func (t tailState) prepend(entries []loki.Entry) (tailState, int) {
	if len(t.rows) > 0 {
		oldest := t.rows[0].entry.Time
		have := map[string]bool{}
		for _, r := range t.rows {
			if !r.entry.Time.Equal(oldest) {
				break
			}
			have[r.entry.Key()] = true
		}
		entries = slices.DeleteFunc(slices.Clone(entries), func(e loki.Entry) bool {
			return e.Time.After(oldest) || (e.Time.Equal(oldest) && have[e.Key()])
		})
	}
	if len(entries) == 0 {
		return t, 0
	}
	a := t.anchor()
	older := make([]row, len(entries))
	for i, e := range entries {
		older[i] = row{id: t.nextID, entry: e, level: loki.DetectLevel(e), stream: e.Labels.String()}
		t.nextID++
		t.track(older[i], 1)
	}
	t.rows = append(older, t.rows...)
	t.total += uint64(len(older))
	t = t.rebuildViewAt(a)
	t = t.refreshLabels()
	return t.scroll(), len(entries)
}

// anchor is where the cursor and the top of the screen are, by row, so they can be put back on
// the same rows once the view is rebuilt.
type anchor struct {
	ok          bool
	cursor, top row
}

// anchor notes the rows under the cursor and at the top of the screen. It has to be taken before
// the rows change, the view indexes them.
func (t tailState) anchor() anchor {
	if t.follow || t.cursor < 0 || t.cursor >= len(t.view) {
		return anchor{}
	}
	a := anchor{ok: true, cursor: t.visible(t.cursor), top: t.visible(t.cursor)}
	if t.offset < len(t.view) {
		a.top = t.visible(t.offset)
	}
	return a
}

// rebuildView lists the rows to show again, keeping the cursor and the top of the screen on the
// rows they were on, or the ones nearest in time when those are no longer shown.
func (t tailState) rebuildView() tailState {
	return t.rebuildViewAt(t.anchor())
}

func (t tailState) rebuildViewAt(a anchor) tailState {
	view := make([]int, 0, len(t.rows))
	for i, r := range t.rows {
		if t.match(r) {
			view = append(view, i)
		}
	}
	t.view = view
	if a.ok {
		t.cursor = t.find(a.cursor)
		t.offset = t.find(a.top)
	}
	return t.scroll()
}

// find returns where r is in view, or the first row shown after it in time.
func (t tailState) find(r row) int {
	i := sort.Search(len(t.view), func(i int) bool { return !t.visible(i).entry.Time.Before(r.entry.Time) })
	for j := i; j < len(t.view) && t.visible(j).entry.Time.Equal(r.entry.Time); j++ {
		if t.visible(j).id == r.id {
			return j
		}
	}
	return min(i, max(len(t.view)-1, 0))
}

// trim drops the oldest tenth of the rows once the buffer is full, so it is not done on every line.
func (t tailState) trim() tailState {
	if t.limit <= 0 || len(t.rows) <= t.limit {
		return t
	}
	drop := len(t.rows) - t.limit + t.limit/10
	for _, r := range t.rows[:drop] {
		t.track(r, -1)
		delete(t.pins, r.id)
	}
	t.rows = slices.Clone(t.rows[drop:])
	view := make([]int, 0, len(t.view))
	removed := 0
	for _, i := range t.view {
		if i < drop {
			removed++
			continue
		}
		view = append(view, i-drop)
	}
	t.view = view
	t.cursor = max(0, t.cursor-removed)
	t.offset = max(0, t.offset-removed)
	return t
}

// track counts the lines of each stream, for the header and the labels that tell them apart.
func (t tailState) track(r row, delta int) {
	s, ok := t.streams[r.stream]
	if !ok {
		s = &streamInfo{labels: r.entry.Labels}
		t.streams[r.stream] = s
	}
	s.count += delta
	if s.count <= 0 {
		delete(t.streams, r.stream)
	}
}

// refreshLabels works out which labels tell the streams apart and how wide they are.
func (t tailState) refreshLabels() tailState {
	sets := make([]loki.Labels, 0, len(t.streams))
	for _, s := range t.streams {
		sets = append(sets, s.labels)
	}
	varying := loki.VaryingLabels(sets)
	if !slices.Equal(varying, t.varying) {
		t.varying = varying
		for _, s := range t.streams {
			s.text = ""
		}
	}
	width := 0
	for _, s := range t.streams {
		if s.text == "" && len(varying) > 0 {
			values := make([]string, 0, len(varying))
			for _, name := range varying {
				if v, ok := s.labels[name]; ok {
					values = append(values, styles.Clean(v))
				}
			}
			s.text = strings.Join(values, " ")
		}
		width = max(width, ansi.StringWidth(s.text))
	}
	t.labelWidth = min(width, contentWidth/4, 40)
	return t
}

// height is how many screen lines the row at i of view takes.
func (t tailState) height(i int) int {
	if !t.wrap {
		return 1
	}
	return len(wrapLine(styles.Clean(t.visible(i).entry.Line), t.lineWidth()))
}

// wrapLine breaks a line into pieces of width columns.
func wrapLine(s string, width int) []string {
	if ansi.StringWidth(s) <= width {
		return []string{s}
	}
	return strings.Split(ansi.Hardwrap(s, width, true), "\n")
}

// scroll keeps the cursor on screen, on the newest line when following, and the screen filled.
func (t tailState) scroll() tailState {
	n := len(t.view)
	if n == 0 {
		t.cursor, t.offset = 0, 0
		return t
	}
	if t.follow {
		t.cursor = n - 1
	}
	t.cursor = max(0, min(t.cursor, n-1))
	t.offset = max(0, min(t.offset, n-1))
	h := tailBodyHeight()
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	used := 0
	for i := t.offset; i <= t.cursor; i++ {
		used += t.height(i)
	}
	for used > h && t.offset < t.cursor {
		used -= t.height(t.offset)
		t.offset++
	}
	// With room left below the last row, earlier rows move into it.
	used = 0
	for i := t.offset; i < n && used <= h; i++ {
		used += t.height(i)
	}
	for t.offset > 0 && used+t.height(t.offset-1) <= h {
		t.offset--
		used += t.height(t.offset)
	}
	return t
}

// move puts the cursor delta rows further, following again once it reaches the newest line.
func (t tailState) move(delta int) tailState {
	t.cursor = max(0, min(t.cursor+delta, len(t.view)-1))
	t.follow = len(t.view) > 0 && t.cursor == len(t.view)-1 && delta > 0
	return t.scroll()
}

// setFilter applies a filter, keeping the cursor on its line.
func (t tailState) setFilter(f lineFilter) tailState {
	t.filter = f
	return t.rebuildView()
}

func (t tailState) togglePin() tailState {
	r, ok := t.selected()
	if !ok {
		return t
	}
	if t.pins[r.id] {
		delete(t.pins, r.id)
	} else {
		t.pins[r.id] = true
	}
	if t.pinnedOnly {
		t = t.rebuildView()
	}
	return t
}

// clear empties the buffer, keeping the follow running.
func (t tailState) clear() tailState {
	t.rows, t.view, t.pending = nil, nil, nil
	t.cursor, t.offset = 0, 0
	t.follow = true
	t.pins = map[uint64]bool{}
	t.streams = map[string]*streamInfo{}
	t.varying = nil
	t.olderEnd = time.Time{}
	return t
}

// renderTail draws the rows on screen.
func (t tailState) render(focused bool) string {
	h := tailBodyHeight()
	if len(t.view) == 0 {
		return t.renderEmpty(h)
	}
	lines := make([]string, 0, h)
	for i := t.offset; i < len(t.view) && len(lines) < h; i++ {
		lines = append(lines, t.renderRow(i, focused)...)
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	// A wrapped row too tall to fit above the others still fills the room left with its end.
	if room := h - len(lines); room > 0 && t.offset > 0 {
		above := t.renderRow(t.offset-1, focused)
		lines = append(above[max(0, len(above)-room):], lines...)
	}
	return strings.Join(lines, "\n")
}

func (t tailState) renderEmpty(h int) string {
	var msg string
	switch {
	case t.pinnedOnly:
		msg = "no pinned lines, space pins the line under the cursor"
	case t.filter.active() && len(t.rows) > 0:
		msg = fmt.Sprintf("none of the %s match the filter", plural(len(t.rows), "line"))
	case t.paused:
		msg = "paused, p resumes"
	case t.state == loki.Connecting:
		msg = "connecting…"
	case t.state == loki.Failed:
		msg = "the tail stopped, e edits the query"
	default:
		msg = "waiting for lines…"
	}
	return lipgloss.Place(contentWidth, h, lipgloss.Center, lipgloss.Center, styles.Muted(msg))
}

// renderRow draws the row at i of view: one screen line, or with wrapping on, as many as it needs.
func (t tailState) renderRow(i int, focused bool) []string {
	r := t.visible(i)
	selected := i == t.cursor
	var bg color.Color
	if selected {
		bg = styles.P.Surface
		if focused {
			bg = styles.P.Selection
		}
	}

	marker := pad(1, bg)
	if selected {
		tone := styles.ToneMuted
		if focused {
			tone = styles.ToneAccent
		}
		marker = styles.S("▌", tone).On(bg)
	}
	pin := pad(1, bg)
	if t.pins[r.id] {
		pin = styles.B("•", styles.ToneAccent).On(bg)
	}

	timeCell := cell{styles.S(r.entry.Time.Local().Format(clockFormat), styles.ToneFaint)}
	levelCell := cell{styles.S(r.level.String(), levelTone(r.level))}
	if selected {
		timeCell = lift(timeCell)
	}

	var prefix strings.Builder
	prefix.WriteString(marker)
	prefix.WriteString(pin)
	prefix.WriteString(pad(1, bg))
	prefix.WriteString(renderCell(timeCell, column{width: timeWidth}, bg))
	prefix.WriteString(pad(1, bg))
	prefix.WriteString(renderCell(levelCell, column{width: levelWidth}, bg))
	prefix.WriteString(pad(1, bg))
	if lw := t.labelsShown(); lw > 0 {
		text := ""
		if s, ok := t.streams[r.stream]; ok {
			text = s.text
		}
		prefix.WriteString(renderCell(cell{styles.S(text, streamTone(r.stream))}, column{width: lw}, bg))
		prefix.WriteString(pad(1, bg))
	}

	width := t.lineWidth()
	line := styles.Clean(r.entry.Line)
	if !t.wrap {
		return []string{prefix.String() + renderCell(t.highlight(line), column{width: width}, bg) + pad(1, bg)}
	}
	pieces := wrapLine(line, width)
	indent := pad(contentWidth-width-1, bg)
	out := make([]string, len(pieces))
	for j, piece := range pieces {
		lead := indent
		if j == 0 {
			lead = prefix.String()
		}
		out[j] = lead + renderCell(t.highlight(piece), column{width: width}, bg) + pad(1, bg)
	}
	return out
}

// highlight splits a line into spans, with the text the filter matches set apart.
func (t tailState) highlight(line string) cell {
	if !t.filter.active() || t.filter.exclude {
		return cell{{Text: line, Tone: styles.ToneBody}}
	}
	var c cell
	last := 0
	for _, m := range t.filter.re.FindAllStringIndex(line, -1) {
		if m[0] == m[1] {
			continue
		}
		if m[0] > last {
			c = append(c, styles.Span{Text: line[last:m[0]], Tone: styles.ToneBody})
		}
		c = append(c, styles.Span{Text: line[m[0]:m[1]], Tone: styles.ToneWarm, Bold: true})
		last = m[1]
	}
	if last < len(line) {
		c = append(c, styles.Span{Text: line[last:], Tone: styles.ToneBody})
	}
	return c
}

// position describes where the cursor is, e.g. "12 of 240".
func (t tailState) position() string {
	if len(t.view) == 0 {
		return ""
	}
	return formatCount(uint64(t.cursor+1)) + " of " + formatCount(uint64(len(t.view)))
}

// startTail follows query from scratch, stopping the follow before it.
func (m model) startTail(query string) (model, tea.Cmd) {
	m = m.stopTail()
	t := newTailState(m.config.Buffer)
	t.query = query
	t.gen = m.tail.gen + 1
	t.wrap, t.hideLabels = m.tail.wrap, m.tail.hideLabels
	t.filterInput = m.tail.filterInput
	t.filterInput.SetValue("")
	ctx, cancel := context.WithCancel(m.context)
	t.cancel = cancel
	t.events = commands.Follow(ctx, m.client, query, m.config.Since, m.config.Limit)
	t.running = true
	t.state = loki.Connecting
	t.started = time.Now()
	m.tail = t
	m.error = ""
	m = m.SwitchPage(tailView)
	return m, tea.Batch(commands.WaitTail(t.events, t.gen), commands.ScheduleClock(t.gen))
}

// stopTail ends the follow, if one runs. Messages it still sends carry an old generation and are
// ignored.
func (m model) stopTail() model {
	if m.tail.cancel != nil {
		m.tail.cancel()
	}
	m.tail.cancel = nil
	m.tail.running = false
	m.tail.gen++
	return m
}

// onTail takes in what the follow reported.
func (m model) onTail(msg messages.TailMsg) (model, tea.Cmd) {
	if msg.Gen != m.tail.gen {
		return m, nil
	}
	t := m.tail
	t.dropped += uint64(msg.Dropped)
	t = t.add(msg.Entries, time.Now())
	var cmd tea.Cmd
	switch {
	case msg.Closed:
		t.running = false
	case msg.State == loki.Failed:
		t.running = false
		t.state, t.err = msg.State, msg.Err
		m.error = "the tail stopped: " + loki.Describe(msg.Err)
	case msg.State == loki.Retrying && t.state != loki.Retrying:
		t.state, t.err, t.retryAt = msg.State, msg.Err, msg.RetryAt
		if msg.Err != nil && !time.Now().After(msg.RetryAt) {
			m.tail = t
			m, cmd = m.setStatus("tail interrupted: "+loki.Describe(msg.Err), styles.ToneWarning)
			t = m.tail
		}
	default:
		t.state, t.err, t.retryAt = msg.State, msg.Err, msg.RetryAt
	}
	m.tail = t
	if t.running {
		return m, tea.Batch(cmd, commands.WaitTail(t.events, t.gen))
	}
	return m, cmd
}

// onOlder takes in a page of older lines.
func (m model) onOlder(msg messages.OlderLoadedMsg) (model, tea.Cmd) {
	if msg.Gen != m.tail.gen {
		return m, nil
	}
	m.tail.older = false
	m.loading = false
	if msg.Err != nil {
		return m.setStatus("could not load older lines: "+loki.Describe(msg.Err), styles.ToneDanger)
	}
	t, added := m.tail.prepend(msg.Entries)
	if added == 0 {
		t.olderEnd = msg.Start
		m.tail = t
		return m.setStatus(fmt.Sprintf("no lines in the %s before %s, ↑ looks further back",
			formatSince(m.config.Since), msg.End.Local().Format("15:04:05")), styles.ToneWarning)
	}
	t.olderEnd = t.rows[0].entry.Time
	t = t.move(-1)
	m.tail = t
	return m.setStatus("loaded "+plural(added, "older line"), styles.ToneSuccess)
}

// loadOlder asks for the lines before the oldest one in the buffer.
func (m model) loadOlder() (model, tea.Cmd) {
	t := &m.tail
	if t.older || t.pinnedOnly || t.query == "" {
		return m, nil
	}
	end := t.olderEnd
	if end.IsZero() {
		if len(t.rows) > 0 {
			end = t.rows[0].entry.Time
		} else {
			end = t.started.Add(-m.config.Since)
		}
	}
	t.older = true
	m, cmd := m.setStatus("loading older lines…", styles.ToneSuccess)
	return m, tea.Batch(cmd, commands.LoadOlder(m.context, m.client, t.query, end, m.config.Since, t.gen))
}

func (m model) TailUpdate(msg tea.Msg) (model, tea.Cmd) {
	t := &m.tail
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	if t.filtering {
		switch keyMsg.String() {
		case "enter":
			f, err := newLineFilter(t.filterInput.Value())
			if err != nil {
				return m.setStatus("invalid regular expression: "+err.Error(), styles.ToneDanger)
			}
			t.filtering = false
			t.filterInput.Blur()
			m.tail = t.setFilter(f)
			return m, nil
		case "esc":
			t.filtering = false
			t.filterInput.Blur()
			t.filterInput.SetValue(t.filter.text)
			return m, nil
		}
		var cmd tea.Cmd
		t.filterInput, cmd = t.filterInput.Update(msg)
		// Plain text filters as it is typed, a regular expression once it is complete.
		if f, err := newLineFilter(t.filterInput.Value()); err == nil {
			m.tail = t.setFilter(f)
		}
		return m, cmd
	}

	switch {
	case key.Matches(keyMsg, m.keys.Back):
		switch {
		case t.filter.active():
			t.filterInput.SetValue("")
			m.tail = t.setFilter(lineFilter{})
			return m, nil
		case t.pinnedOnly:
			t.pinnedOnly = false
			m.tail = t.rebuildView()
			return m, nil
		}
		return m.backToPicker()
	case key.Matches(keyMsg, m.keys.Quit):
		return m.backToPicker()
	case key.Matches(keyMsg, m.keys.Filter):
		t.filtering = true
		t.filterInput.SetValue(t.filter.text)
		t.filterInput.CursorEnd()
		return m, t.filterInput.Focus()
	case key.Matches(keyMsg, m.keys.Edit):
		return m.openEditor(t.query)
	case key.Matches(keyMsg, m.keys.View):
		if _, ok := t.selected(); ok {
			return m.openDetails(t.cursor)
		}
		return m, nil
	case key.Matches(keyMsg, m.keys.Select):
		m.tail = t.togglePin()
		return m, nil
	case key.Matches(keyMsg, m.keys.Pinned):
		t.pinnedOnly = !t.pinnedOnly
		m.tail = t.rebuildView()
		return m, nil
	case key.Matches(keyMsg, m.keys.Follow):
		t.follow = !t.follow
		m.tail = t.scroll()
		return m, nil
	case key.Matches(keyMsg, m.keys.Pause):
		t.paused = !t.paused
		if !t.paused {
			pending := t.pending
			t.pending = nil
			m.tail = t.insert(pending)
		}
		return m, nil
	case key.Matches(keyMsg, m.keys.Wrap):
		t.wrap = !t.wrap
		m.tail = t.scroll()
		return m, nil
	case key.Matches(keyMsg, m.keys.Labels):
		t.hideLabels = !t.hideLabels
		m.tail = t.scroll()
		return m, nil
	case key.Matches(keyMsg, m.keys.CopyToClipboard):
		if r, ok := t.selected(); ok {
			return m, commands.CopyToClipboard(r.entry.Line)
		}
		return m, nil
	case key.Matches(keyMsg, m.keys.ClearBuffer):
		m.tail = t.clear()
		return m.setStatus("cleared the lines, the tail goes on", styles.ToneSuccess)
	case key.Matches(keyMsg, m.keys.Stream):
		if r, ok := t.selected(); ok {
			return m.startTail(loki.StreamSelector(r.entry.Labels))
		}
		return m, nil
	}

	switch keyMsg.String() {
	case "up", "k":
		if t.cursor == 0 && len(t.view) > 0 || len(t.view) == 0 {
			return m.loadOlder()
		}
		m.tail = t.move(-1)
	case "down", "j":
		m.tail = t.move(1)
	case "pgup":
		m.tail = t.move(-tailBodyHeight())
	case "pgdown":
		m.tail = t.move(tailBodyHeight())
	case "home", "g":
		m.tail = t.move(-len(t.view))
	case "end", "G":
		m.tail = t.move(len(t.view))
	}
	return m, nil
}

// backToPicker stops the tail and returns to the labels, loading them when they never were.
func (m model) backToPicker() (model, tea.Cmd) {
	m = m.stopTail()
	m = m.SwitchPage(labelPicker)
	if !m.picker.loaded {
		m.loading = true
		m.loadingMsg = "loading labels…"
		return m, tea.Batch(commands.LoadLabels(m.context, m.client, m.config.Since), m.spinner.Tick)
	}
	return m, nil
}

func (m model) TailView() string {
	return m.tail.render(!m.tail.filtering && !m.editing)
}
