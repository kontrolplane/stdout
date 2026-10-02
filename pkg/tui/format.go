package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/stdout/pkg/loki"
	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

const (
	clockFormat = "15:04:05.000"
	timeFormat  = "2006-01-02 15:04:05.000000000"
)

// formatAgo renders a timestamp relative to now, e.g. "3m ago".
func formatAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// formatSince renders a lookback window, e.g. "1h" or "15m", without its zero units.
func formatSince(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatCount(n uint64) string {
	s := strconv.FormatUint(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// compactCount renders a count exactly up to 9,999,999 and shortened past it, e.g. 12.3M. It
// rounds down, so a count never reads as more than it is.
func compactCount(n uint64) string {
	if n <= 9_999_999 {
		return formatCount(n)
	}
	v, unit := float64(n)/1e6, "M"
	for _, u := range []string{"B", "T"} {
		if v < 1000 {
			break
		}
		v, unit = v/1000, u
	}
	if v < 100 {
		return strconv.FormatFloat(math.Floor(v*10)/10, 'f', 1, 64) + unit
	}
	return strconv.FormatFloat(math.Floor(v), 'f', 0, 64) + unit
}

// formatRate renders lines per second, with a decimal below ten.
func formatRate(r float64) string {
	switch {
	case r <= 0:
		return "0/s"
	case r < 10:
		return strconv.FormatFloat(math.Round(r*10)/10, 'f', -1, 64) + "/s"
	}
	return compactCount(uint64(math.Round(r))) + "/s"
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%s %s", formatCount(uint64(n)), noun)
	}
	return fmt.Sprintf("%s %ss", formatCount(uint64(n)), noun)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, width int) string {
	return ansi.Truncate(s, width, "…")
}

// levelTone is the tone a level is painted in.
func levelTone(l loki.Level) styles.Tone {
	switch l {
	case loki.LevelFatal, loki.LevelError:
		return styles.ToneDanger
	case loki.LevelWarn:
		return styles.ToneWarning
	case loki.LevelInfo:
		return styles.ToneSuccess
	case loki.LevelDebug:
		return styles.ToneInfo
	}
	return styles.ToneFaint
}

// streamTones are the tones streams are told apart by in the tail. Success, warning and danger
// are left out, they belong to the levels.
var streamTones = []styles.Tone{styles.ToneAccent, styles.ToneInfo, styles.ToneWarm, styles.ToneText, styles.ToneMuted}

// streamTone picks a tone for a stream from its key, the same one every time.
func streamTone(key string) styles.Tone {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return streamTones[h%uint32(len(streamTones))]
}

func initFilterInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.SetWidth(50)
	ti.SetStyles(styles.TextInput())
	return ti
}

// spread places left and right on one line of the given width.
func spread(left, right string, width int) string {
	gap := width - styledWidth(left) - styledWidth(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func verticalDivider(height int) string {
	lines := make([]string, height)
	for i := range lines {
		lines[i] = "│"
	}
	return lipgloss.NewStyle().Foreground(styles.P.Rule).Render(strings.Join(lines, "\n"))
}

// clip cuts s to height lines of at most width columns.
func clip(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:max(height, 0)]
	}
	for i, line := range lines {
		if styledWidth(line) > width {
			lines[i] = ansi.Truncate(line, width, "…")
		}
	}
	return strings.Join(lines, "\n")
}

// frame draws a rounded border around body, with title and meta set into the top edge and foot
// into the bottom one.
func frame(title, meta, foot, body string) string {
	border := lipgloss.NewStyle().Foreground(styles.P.Rule)
	edge := func(n int) string {
		if n < 1 {
			n = 1
		}
		return border.Render(strings.Repeat("─", n))
	}

	metaWidth := 0
	if meta != "" {
		meta = " " + ansi.Truncate(meta, frameWidth/3, "…") + " "
		metaWidth = lipgloss.Width(meta)
	}
	title = ansi.Truncate(title, frameWidth-7-metaWidth, "…")
	top := border.Render("╭─ ") + title + " " +
		edge(frameWidth-5-lipgloss.Width(title)-1-metaWidth) + meta + border.Render("─╮")

	footWidth := 0
	if foot != "" {
		foot = " " + ansi.Truncate(foot, frameWidth-6, "…") + " "
		footWidth = lipgloss.Width(foot)
	}
	bottom := border.Render("╰") + edge(frameWidth-3-footWidth) + foot + border.Render("─╯")

	// The body fills the content area: cut to it, with every line centred in it as lipgloss.Place
	// would, and a blank line above and below. Each line is measured once, this runs every frame.
	lines := strings.Split(body, "\n")
	lines = lines[:min(len(lines), contentHeight)]
	side := border.Render("│")
	blank := side + strings.Repeat(" ", contentWidth) + side

	var b strings.Builder
	b.Grow(len(body) + (contentHeight+4)*(contentWidth+40))
	b.WriteString(top)
	b.WriteString("\n")
	b.WriteString(blank)
	for i := range contentHeight {
		b.WriteString("\n")
		b.WriteString(side)
		if i >= len(lines) {
			b.WriteString(strings.Repeat(" ", contentWidth))
		} else {
			line := lines[i]
			w := styledWidth(line)
			if w > contentWidth {
				line = ansi.Truncate(line, contentWidth, "…")
				w = styledWidth(line)
			}
			gap := max(0, contentWidth-w)
			b.WriteString(strings.Repeat(" ", gap/2))
			b.WriteString(line)
			b.WriteString(strings.Repeat(" ", gap-gap/2))
		}
		b.WriteString(side)
	}
	b.WriteString("\n")
	b.WriteString(blank)
	b.WriteString("\n")
	b.WriteString(bottom)
	return b.String()
}

const dialogPadX = 4

// dialogTextWidth is the widest a line of dialog text gets before it wraps.
func dialogTextWidth() int {
	return min(contentWidth-2-2*dialogPadX, 100)
}

// dialog draws a card headed by title in tone, to be set over the page with overlay.
func dialog(title string, tone styles.Tone, body ...string) string {
	heading := styles.Render(styles.B("▲ ", tone), styles.B(title, tone))
	width := dialogTextWidth()
	lines := []string{heading, ""}
	for _, block := range body {
		for _, line := range strings.Split(block, "\n") {
			if lipgloss.Width(line) > width {
				line = lipgloss.Wrap(line, width, "")
			}
			lines = append(lines, line)
		}
	}
	content := lipgloss.JoinVertical(lipgloss.Center, lines...)
	padY := 1
	if lipgloss.Height(content)+2+2*padY > contentHeight {
		padY = 0
	}
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.P.Color(tone)).
		Padding(padY, dialogPadX).
		Render(content)
	return card
}

// overlay sets card in the middle of the content area, over the page, which recedes behind it.
func overlay(page, card string) string {
	lines := strings.Split(ansi.Strip(page), "\n")
	dim := sgr(styles.P.Rule, false, nil)
	for i, l := range lines {
		lines[i] = dim + l + ansi.ResetStyle
	}
	// A ring of blank cells keeps the page's text off the card's edge.
	card = lipgloss.NewStyle().Padding(0, 1).Render(card)
	w, h := lipgloss.Width(card), lipgloss.Height(card)
	c := lipgloss.NewCanvas(contentWidth, contentHeight)
	c.Compose(lipgloss.NewCompositor(
		lipgloss.NewLayer(strings.Join(lines, "\n")),
		lipgloss.NewLayer(card).X(max(0, (contentWidth-w)/2)).Y(max(0, (contentHeight-h)/2)).Z(1),
	))
	// The canvas leaves out the blank cells at the end of a line, and the frame centres a line
	// shorter than the content area, which would move the card off the middle.
	out := strings.Split(c.Render(), "\n")
	for i, l := range out {
		out[i] = l + strings.Repeat(" ", max(0, contentWidth-styledWidth(l)))
	}
	return strings.Join(out, "\n")
}

// ErrorView shows the error in a card.
func (m model) ErrorView() string {
	text := styles.CleanBlock(m.error)
	width := min(80, dialogTextWidth(), lipgloss.Width(text))
	lines := strings.Split(styles.Fg(styles.ToneBody).Width(width).Render(text), "\n")
	// Room for the heading, the hint below and the card's edges.
	if room := max(1, contentHeight-6); len(lines) > room {
		lines = append(lines[:room-1], styles.Faint("…"))
	}
	return dialog("error", styles.ToneDanger, strings.Join(lines, "\n"), "", styles.Faint("press any key to continue"))
}

// paintLines writes s in tone, styling each line on its own so a wrapped token keeps its colour
// on every line without being padded into a block.
func paintLines(b *strings.Builder, tone styles.Tone, s string) {
	prefix := sgr(styles.P.Color(tone), false, nil)
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteString(ansi.ResetStyle)
	}
}

// highlightJSON colours pretty printed JSON: keys carry the text, strings the accent, numbers and
// literals the warm ink, and punctuation recedes.
func highlightJSON(s string) string {
	var b strings.Builder
	paint := func(tone styles.Tone, t string) { paintLines(&b, tone, t) }

	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(s))
			k := j
			for k < len(s) && s[k] == ' ' {
				k++
			}
			if k < len(s) && s[k] == ':' {
				paint(styles.ToneText, s[i:j])
			} else {
				paint(styles.ToneAccent, s[i:j])
			}
			i = j
		case c == '-' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(s) && strings.IndexByte("0123456789.eE+-", s[j]) >= 0 {
				j++
			}
			paint(styles.ToneWarm, s[i:j])
			i = j
		case c == 't' || c == 'f' || c == 'n':
			j := i
			for j < len(s) && s[j] >= 'a' && s[j] <= 'z' {
				j++
			}
			paint(styles.ToneWarm, s[i:j])
			i = max(j, i+1)
		case c == ' ' || c == '\n':
			b.WriteByte(c)
			i++
		default:
			paint(styles.ToneFaint, string(c))
			i++
		}
	}
	return b.String()
}
