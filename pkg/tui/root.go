package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/stdout/pkg/client"
	keys "github.com/kontrolplane/stdout/pkg/keys"
	"github.com/kontrolplane/stdout/pkg/loki"
	"github.com/kontrolplane/stdout/pkg/tui/commands"
	"github.com/kontrolplane/stdout/pkg/tui/messages"
	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

func NewModel(projectName, programName string, c *loki.Client, info client.Info, config Config) tea.Model {
	m := newModel(projectName, programName, config)
	m.client = c
	m.info = info
	if config.Query != "" {
		m, _ = m.startTail(config.Query)
	}
	return m
}

func newModel(projectName, programName string, config Config) model {
	editor := initFilterInput("{app=\"api\"} |= \"error\"")
	editor.CharLimit = 4000
	return model{
		projectName: projectName,
		programName: programName,
		config:      config,
		page:        labelPicker,
		context:     context.Background(),
		loading:     true,
		loadingMsg:  "loading labels…",
		spinner:     spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		keys:        keys.Keys,
		editor:      editor,
		picker:      newPickerState(),
		tail:        newTailState(config.Buffer),
	}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{commands.LoadBuildInfo(m.context, m.client)}
	if m.page == tailView {
		cmds = append(cmds, commands.WaitTail(m.tail.events, m.tail.gen), commands.ScheduleClock(m.tail.gen))
	} else {
		cmds = append(cmds, commands.LoadLabels(m.context, m.client, m.config.Since), m.spinner.Tick)
	}
	return tea.Batch(cmds...)
}

// textInputActive reports whether keystrokes are currently going into a text field,
// in which case single character shortcuts must not trigger.
func (m model) textInputActive() bool {
	if m.editing {
		return true
	}
	switch m.page {
	case labelPicker:
		return m.picker.filtering
	case tailView:
		return m.tail.filtering
	}
	return false
}

// setStatus shows text in the footer for a few seconds, in the success, warning or danger tone.
func (m model) setStatus(text string, tone styles.Tone) (model, tea.Cmd) {
	m.statusGen++
	m.statusMsg = text
	m.statusTone = tone
	return m, commands.ClearStatusAfter(4*time.Second, m.statusGen)
}

// hasControlChars reports whether s holds control characters other than line breaks and tabs.
func hasControlChars(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r'
	})
}

// openEditor puts the query editor in the footer, holding query.
func (m model) openEditor(query string) (model, tea.Cmd) {
	m.editing = true
	m.editor.SetValue(query)
	m.editor.CursorEnd()
	return m, m.editor.Focus()
}

func (m model) editorUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		query := strings.TrimSpace(m.editor.Value())
		if query == "" || query == "{}" {
			return m.setStatus("write a query first, like {app=\"api\"}", styles.ToneWarning)
		}
		m.editing = false
		m.editor.Blur()
		return m.startTail(query)
	case "esc":
		m.editing = false
		m.editor.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		setLayout(msg.Width, msg.Height)
		return m.resize(), nil

	case spinner.TickMsg:
		if !m.loading {
			m.spinning = false
			return m, nil
		}
		m.spinning = true
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case messages.BuildInfoMsg:
		// A gateway may not pass the build info on, the version then reads as unknown.
		m.build, m.buildOK = msg.Info, true
		return m, nil

	case messages.LabelsLoadedMsg:
		return m.onLabelsLoaded(msg)

	case messages.ValuesTickMsg:
		return m.onValuesTick(msg)

	case messages.ValuesLoadedMsg:
		return m.onValuesLoaded(msg)

	case messages.TailMsg:
		return m.onTail(msg)

	case messages.OlderLoadedMsg:
		return m.onOlder(msg)

	case messages.ClockTickMsg:
		// Redraws the rate and the retry countdown while the tail runs.
		if msg.Gen != m.tail.gen || !m.tail.running {
			return m, nil
		}
		return m, commands.ScheduleClock(m.tail.gen)

	case messages.ClipboardCopiedMsg:
		var cmds []tea.Cmd
		if msg.Err != nil {
			cmds = append(cmds, tea.SetClipboard(msg.Text))
		}
		if hasControlChars(msg.Text) {
			m, cmd = m.setStatus("copied, the line contains control characters", styles.ToneWarning)
		} else {
			m, cmd = m.setStatus("copied to clipboard", styles.ToneSuccess)
		}
		return m, tea.Batch(append(cmds, cmd)...)

	case messages.StatusClearMsg:
		if msg.Gen == m.statusGen {
			m.statusMsg = ""
		}
		return m, nil

	case tea.KeyPressMsg:
		if key.Matches(msg, m.keys.ForceQuit) {
			m = m.stopTail()
			return m, tea.Quit
		}
		if m.tooSmall() {
			return m, nil
		}
		if m.error != "" {
			m.error = ""
			return m, nil
		}
		if m.editing {
			return m.editorUpdate(msg)
		}
		if !m.textInputActive() && key.Matches(msg, m.keys.Help) {
			m.showHelp = !m.showHelp
			return m, nil
		}
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		if m.loading {
			if key.Matches(msg, m.keys.Quit) {
				return m, tea.Quit
			}
			return m, nil
		}
	}

	switch m.page {
	case labelPicker:
		return m.PickerUpdate(msg)
	case tailView:
		return m.TailUpdate(msg)
	case lineDetails:
		return m.DetailsUpdate(msg)
	}
	return m, nil
}

func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = m.programName
	if m.page != labelPicker && m.tail.query != "" {
		// Several tails in tabs of one terminal are told apart by their queries.
		v.WindowTitle += " · " + truncate(styles.Clean(m.tail.query), 60)
	}
	v.ForegroundColor = styles.P.Text
	if styles.Paint {
		v.BackgroundColor = styles.P.Base
	}
	return v
}

// tooSmall reports whether the terminal is smaller than the layout needs.
func (m model) tooSmall() bool {
	return m.width > 0 && (m.width < minContentWidth+chromeWidth || m.height < minContentHeight+chromeHeight)
}

// tooSmallView asks for a larger terminal, in place of a layout that would not fit.
func (m model) tooSmallView() string {
	notice := lipgloss.JoinVertical(lipgloss.Center,
		styles.Render(styles.B("terminal too small", styles.ToneWarning)),
		styles.Muted(fmt.Sprintf("%d×%d, needs %d×%d", m.width, m.height, minContentWidth+chromeWidth, minContentHeight+chromeHeight)),
		styles.Faint("ctrl+c to quit"),
	)
	return clip(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, notice), m.width, m.height)
}

func (m model) render() string {
	if m.tooSmall() {
		return m.tooSmallView()
	}

	meta, foot := m.frameMeta()
	mainView := m.renderHeader() + "\n\n" +
		frame(styles.Render(m.breadcrumb()...), meta, foot, m.content()) + "\n" +
		m.renderFooter()

	if m.width == 0 {
		return mainView
	}
	return place(m.width, m.height, mainView)
}

// content renders what the frame holds: the page, with the help or an error over it, or the
// loading notice in its place.
func (m model) content() string {
	page := m.pageView()
	switch {
	case m.showHelp:
		return overlay(page, m.renderHelp())
	case m.error != "":
		return overlay(page, m.ErrorView())
	}
	return page
}

func (m model) pageView() string {
	if m.loading {
		return m.LoadingView()
	}
	switch m.page {
	case labelPicker:
		return m.PickerView()
	case tailView:
		return m.TailView()
	case lineDetails:
		return m.DetailsView()
	}
	return ""
}

func (m model) LoadingView() string {
	msg := m.loadingMsg
	if msg == "" {
		msg = "loading…"
	}
	return lipgloss.Place(contentWidth, contentHeight, lipgloss.Center, lipgloss.Center,
		styles.Accent(m.spinner.View())+" "+styles.Muted(truncate(styles.Clean(msg), contentWidth-4)))
}

// frameMeta returns what is set into the frame's edges: the active filter on top, and at the
// bottom the follow, the cursor position and the state of the tail.
func (m model) frameMeta() (string, string) {
	var meta string
	var foot []string
	switch m.page {
	case labelPicker:
		p := m.picker
		if f := p.filters[p.focus]; f != "" {
			meta = styles.Render(styles.S("filter ", styles.ToneFaint), styles.S(f, styles.ToneText))
		}
		t := p.labelsTable
		if p.focus == focusValues {
			t = p.valuesTable
		}
		if pos := t.position(); pos != "" && !m.loading {
			foot = append(foot, styles.Faint(pos))
		}
	case tailView, lineDetails:
		t := m.tail
		if t.filter.active() {
			spans := []styles.Span{styles.S("filter ", styles.ToneFaint)}
			if t.filter.exclude {
				spans = append(spans, styles.B("not ", styles.ToneDanger), styles.S(strings.TrimPrefix(t.filter.text, "!"), styles.ToneText))
			} else {
				spans = append(spans, styles.S(t.filter.text, styles.ToneText))
			}
			meta = styles.Render(spans...)
		}
		if m.page == tailView {
			switch {
			case t.follow:
				foot = append(foot, styles.Render(styles.S("↓ ", styles.ToneAccent), styles.S("following", styles.ToneMuted)))
			case t.unseen > 0:
				foot = append(foot, styles.Render(styles.B("↓ "+compactCount(uint64(t.unseen))+" new", styles.ToneAccent),
					styles.S(" G jumps there", styles.ToneFaint)))
			default:
				foot = append(foot, styles.Faint("follow off"))
			}
			if t.wrap {
				foot = append(foot, styles.Faint("wrap"))
			}
			if n := len(t.pins); n > 0 {
				foot = append(foot, styles.Faint(plural(n, "pin")))
			}
			if pos := t.position(); pos != "" {
				foot = append(foot, styles.Faint(pos))
			}
		}
		foot = append(foot, m.followState())
	}
	return meta, strings.Join(foot, styles.Faint(" · "))
}

func (m model) renderFooter() string {
	var status string
	if m.statusMsg != "" {
		switch m.statusTone {
		case styles.ToneDanger:
			status = styles.Render(styles.S("✗ ", styles.ToneDanger), styles.S(m.statusMsg, styles.ToneDanger))
		case styles.ToneWarning:
			status = styles.Render(styles.S("▲ ", styles.ToneWarning), styles.S(m.statusMsg, styles.ToneWarning))
		default:
			status = styles.Render(styles.S("✓ ", styles.ToneSuccess), styles.S(m.statusMsg, styles.ToneBody))
		}
	}
	if w := lipgloss.Width(status); w > frameWidth/2 {
		status = ansi.Truncate(status, frameWidth/2, "…")
	}
	width := frameWidth - 2 - lipgloss.Width(status) - 2
	left := ansi.Truncate(m.renderFooterLeft(width), width, "…")
	return " " + spread(left, status+" ", frameWidth-1)
}

// renderFooterLeft renders what the footer shows next to the status, in at most width columns.
func (m model) renderFooterLeft(width int) string {
	switch {
	case m.editing:
		return styles.Accent("› ") + m.editor.View() + "  " + editorHints()
	case m.page == labelPicker && m.picker.filtering:
		return renderFilterBar(m.picker.filterInput.View())
	case m.page == tailView && m.tail.filtering:
		return renderFilterBar(m.tail.filterInput.View())
	}
	return fitHints(width, m.shortHelp()...)
}

func editorHints() string {
	return hints([2]string{"enter", "tail"}, [2]string{"esc", "cancel"})
}

// editorWidth leaves the query editor the footer but for its edges, its prompt, its hints and the
// cell the cursor takes past the end of the text.
func editorWidth() int {
	return frameWidth - 4 - 2 - 2 - styledWidth(editorHints()) - 1
}

func renderFilterBar(inputView string) string {
	return styles.Accent("/ ") + inputView + "  " +
		hints([2]string{"enter", "apply"}, [2]string{"esc", "clear"})
}

// fitHints renders the hints that fit width. Hints are dropped from the end but for the last two,
// help and back or quit, which stay.
func fitHints(width int, pairs ...[2]string) string {
	var key strings.Builder
	fmt.Fprint(&key, width)
	for _, p := range pairs {
		key.WriteString("\x00" + p[0] + "\x00" + p[1])
	}
	return hintsMemo.get(key.String(), func() string {
		pairs = slices.Clone(pairs)
		for len(pairs) > 2 && styledWidth(hints(pairs...)) > width {
			pairs = slices.Delete(pairs, len(pairs)-3, len(pairs)-2)
		}
		return hints(pairs...)
	})
}

var hintsMemo memo

func hints(pairs ...[2]string) string {
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = styles.Key(p[0], p[1])
	}
	return strings.Join(parts, styles.Faint("  ·  "))
}

func (m model) shortHelp() [][2]string {
	if m.error != "" {
		return [][2]string{{"any key", "dismiss"}}
	}
	switch m.page {
	case labelPicker:
		if m.picker.focus == focusLabels {
			return [][2]string{{"enter", "values"}, {"tab", "switch list"}, {"e", "edit query"}, {"/", "filter"}, {"r", "refresh"}, {"?", "help"}, {"q", "quit"}}
		}
		return [][2]string{{"space", "select"}, {"enter", "tail"}, {"!", "exclude"}, {"x", "clear"}, {"e", "edit query"}, {"/", "filter"}, {"?", "help"}, {"q", "quit"}}
	case tailView:
		return [][2]string{{"enter", "open"}, {"space", "pin"}, {"tab", "pinned"}, {"f", "follow"}, {"p", "pause"}, {"w", "wrap"}, {"e", "edit query"}, {"c", "copy"}, {"/", "filter"}, {"?", "help"}, {"q", "back"}}
	case lineDetails:
		h := [][2]string{{"↑/↓", "scroll"}, {"[/]", "previous/next"}, {"space", "pin"}, {"s", "tail stream"}, {"c", "copy"}, {"?", "help"}, {"q", "back"}}
		if m.details.fieldsOverflow() {
			panel := "fields"
			if m.details.onFields {
				panel = "message"
			}
			h = slices.Insert(h, 1, [2]string{"tab", panel})
		}
		return h
	}
	return nil
}

// renderHelp draws the key reference in a card, as roomy as the content area allows.
func (m model) renderHelp() string {
	var card string
	for _, fit := range []struct{ padY, padX, gap int }{{1, 4, 5}, {1, 2, 3}, {0, 2, 3}} {
		card = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(styles.P.RuleBold).
			Padding(fit.padY, fit.padX).
			Render(renderHelpContent(m.page, contentWidth-4-2*fit.padX, contentHeight-2-2*fit.padY, fit.gap))
		if lipgloss.Width(card) <= contentWidth-2 && lipgloss.Height(card) <= contentHeight {
			break
		}
	}
	return card
}

// helpSection is a column of the key reference.
type helpSection struct {
	page  page
	title string
	rows  [][2]string
}

var helpSections = []helpSection{
	{labelPicker, "labels", [][2]string{
		{"↑/k ↓/j", "move"},
		{"tab ←/→", "switch list"},
		{"space", "select value"},
		{"!", "exclude values"},
		{"x / X", "clear / clear all"},
		{"enter", "tail selection"},
		{"e", "write a query"},
		{"/", "filter the list"},
		{"r", "refresh"},
		{"q", "quit"},
	}},
	{tailView, "tail", [][2]string{
		{"↑ at top", "older lines"},
		{"g / G", "oldest / newest"},
		{"n / N", "next / prev problem"},
		{"enter", "open line"},
		{"space", "pin line"},
		{"tab", "pinned only"},
		{"f / p", "follow / pause"},
		{"w / l", "wrap / labels"},
		{"/", "filter lines"},
		{"e", "edit query"},
		{"s", "tail its stream"},
		{"c", "copy line"},
		{"ctrl+l", "clear lines"},
		{"esc / q", "back"},
	}},
	{lineDetails, "line", [][2]string{
		{"↑/↓ g/G", "scroll"},
		{"tab", "fields / message"},
		{"[ / ]", "previous / next"},
		{"space", "pin"},
		{"s", "tail its stream"},
		{"c", "copy line"},
		{"esc / q", "back"},
	}},
}

// renderHelpContent lays the sections out with the one of the current page first: side by side
// when they fit width, else in rows of two, else the current page alone.
func renderHelpContent(current page, width, height, gap int) string {
	title := func(s string, on bool) string {
		tone := styles.ToneMuted
		if on {
			tone = styles.ToneAccent
		}
		return styles.Fg(tone).Bold(true).MarginBottom(1).Render(s)
	}
	keyStyle := styles.Fg(styles.ToneText).Bold(true).Width(10)
	column := func(s helpSection) string {
		lines := []string{title(s.title, s.page == current)}
		for _, r := range s.rows {
			lines = append(lines, keyStyle.Render(r[0])+styles.Muted(r[1]))
		}
		return lipgloss.JoinVertical(lipgloss.Left, lines...)
	}

	columns := []string{}
	for _, s := range helpSections {
		if s.page == current {
			columns = append([]string{column(s)}, columns...)
		} else {
			columns = append(columns, column(s))
		}
	}
	columns = append(columns, lipgloss.JoinVertical(lipgloss.Left,
		title("filters", false),
		keyStyle.Render("timeout")+styles.Muted("contains, any case"),
		keyStyle.Render(`/5\d\d/`)+styles.Muted("regular expression"),
		keyStyle.Render("!healthz")+styles.Muted("hide matching"),
		"",
		styles.Muted("/ filters the lines on"),
		styles.Muted("screen, e changes what"),
		styles.Muted("loki sends, like"),
		styles.Fg(styles.ToneText).Render(`{app="api"} |= "err"`),
	))

	spaced := lipgloss.NewStyle().MarginRight(gap)
	row := func(cols ...string) string {
		for i := range cols[:len(cols)-1] {
			cols[i] = spaced.Render(cols[i])
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
	}
	fits := func(s string) bool { return lipgloss.Width(s) <= width && lipgloss.Height(s) <= height-2 }

	layouts := []string{
		row(slices.Clone(columns)...),
		lipgloss.JoinVertical(lipgloss.Left, row(columns[0], columns[1]), "", row(columns[2], columns[3])),
		row(columns[0], columns[3]),
	}
	body := columns[0]
	for _, l := range layouts {
		if fits(l) {
			body = l
			break
		}
	}
	return lipgloss.JoinVertical(lipgloss.Center, body, "", styles.Faint("press any key to close"))
}
