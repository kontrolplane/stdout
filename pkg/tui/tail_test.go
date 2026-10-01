package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/stdout/pkg/loki"
)

var base = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func entry(sec int, app, line string) loki.Entry {
	return loki.Entry{Time: base.Add(time.Duration(sec) * time.Second), Line: line, Labels: loki.Labels{"app": app}}
}

func entries(n int) []loki.Entry {
	out := make([]loki.Entry, n)
	for i := range out {
		out[i] = entry(i, "api", fmt.Sprintf("line %d", i))
	}
	return out
}

func lines(t tailState) []string {
	out := make([]string, len(t.view))
	for i := range t.view {
		out[i] = t.visible(i).entry.Line
	}
	return out
}

func TestTailInsertKeepsTimeOrder(t *testing.T) {
	s := newTailState(1000)
	s = s.add([]loki.Entry{entry(1, "api", "a"), entry(3, "api", "c")}, base)
	s = s.add([]loki.Entry{entry(2, "web", "b"), entry(4, "api", "d")}, base)
	if got, want := lines(s), []string{"a", "b", "c", "d"}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	if s.cursor != 3 || !s.follow {
		t.Errorf("following tail should sit on the newest line, cursor %d", s.cursor)
	}
	if len(s.streams) != 2 || !slices.Equal(s.varying, []string{"app"}) {
		t.Errorf("streams %d, varying %v", len(s.streams), s.varying)
	}
}

func TestTailCursorStaysOnItsLine(t *testing.T) {
	s := newTailState(1000)
	s = s.add(entries(10), base)
	s = s.move(-3) // on line 6, follow off
	if s.follow {
		t.Fatal("moving up should stop following")
	}
	s = s.add([]loki.Entry{entry(20, "api", "new"), entry(5, "api", "late")}, base)
	if r, _ := s.selected(); r.entry.Line != "line 6" {
		t.Errorf("cursor moved to %q", r.entry.Line)
	}
	s = s.move(len(s.view))
	if !s.follow {
		t.Error("reaching the newest line should follow again")
	}
}

func TestTailTrim(t *testing.T) {
	s := newTailState(100)
	s = s.add(entries(100), base)
	s.pins[s.rows[0].id] = true
	s = s.add([]loki.Entry{entry(200, "api", "over")}, base)
	// 101 lines, over the limit of 100: the 1 over and a tenth more go.
	if len(s.rows) != 90 {
		t.Fatalf("rows = %d, want the oldest tenth dropped", len(s.rows))
	}
	if s.rows[0].entry.Line != "line 11" || len(s.view) != 90 || s.cursor != 89 {
		t.Errorf("first %q, view %d, cursor %d", s.rows[0].entry.Line, len(s.view), s.cursor)
	}
	if len(s.pins) != 0 {
		t.Error("pins of dropped lines should go with them")
	}
	if s.total != 101 {
		t.Errorf("total = %d", s.total)
	}
}

func TestTailFilter(t *testing.T) {
	s := newTailState(1000)
	s = s.add([]loki.Entry{entry(1, "api", "GET /healthz 200"), entry(2, "api", "POST /orders 500"), entry(3, "api", "GET /orders 200")}, base)

	tests := []struct {
		filter string
		want   []string
	}{
		{"orders", []string{"POST /orders 500", "GET /orders 200"}},
		{"ORDERS", []string{"POST /orders 500", "GET /orders 200"}},
		{"/ 5\\d\\d$/", []string{"POST /orders 500"}},
		{"!healthz", []string{"POST /orders 500", "GET /orders 200"}},
		{"", []string{"GET /healthz 200", "POST /orders 500", "GET /orders 200"}},
	}
	for _, tt := range tests {
		f, err := newLineFilter(tt.filter)
		if err != nil {
			t.Fatalf("%q: %v", tt.filter, err)
		}
		if got := lines(s.setFilter(f)); !slices.Equal(got, tt.want) {
			t.Errorf("filter %q: %v, want %v", tt.filter, got, tt.want)
		}
	}
	if _, err := newLineFilter("/[/"); err == nil {
		t.Error("want an error for an invalid regular expression")
	}

	f, _ := newLineFilter("orders")
	s = s.setFilter(f)
	s = s.add([]loki.Entry{entry(4, "api", "GET /healthz 200"), entry(5, "api", "DELETE /orders 204")}, base)
	if got := lines(s); len(got) != 3 || got[2] != "DELETE /orders 204" {
		t.Errorf("new lines should be filtered too: %v", got)
	}
	c := s.highlight("GET /orders and /ORDERS")
	if len(c) != 4 || c[1].Text != "orders" || c[3].Text != "ORDERS" || !c[1].Bold {
		t.Errorf("highlight = %+v", c)
	}
}

func TestTailPins(t *testing.T) {
	s := newTailState(1000)
	s = s.add(entries(5), base)
	s = s.move(-1).togglePin() // line 3
	s = s.move(-2).togglePin() // line 1
	s.pinnedOnly = true
	s = s.rebuildView()
	if got, want := lines(s), []string{"line 1", "line 3"}; !slices.Equal(got, want) {
		t.Fatalf("pinned = %v, want %v", got, want)
	}
	s = s.togglePin()
	if len(s.view) != 1 {
		t.Errorf("unpinning in the pinned view should hide the line, view %v", lines(s))
	}
}

func TestTailPause(t *testing.T) {
	s := newTailState(1000)
	s = s.add(entries(3), base)
	s.paused = true
	s = s.add([]loki.Entry{entry(10, "api", "held")}, base)
	if len(s.rows) != 3 || len(s.pending) != 1 {
		t.Fatalf("rows %d, pending %d", len(s.rows), len(s.pending))
	}
	s.paused = false
	s = s.insert(s.pending)
	if len(s.rows) != 4 {
		t.Errorf("rows %d after resuming", len(s.rows))
	}
}

func TestTailPrependKeepsCursor(t *testing.T) {
	s := newTailState(1000)
	s = s.add([]loki.Entry{entry(10, "api", "a"), entry(11, "api", "b")}, base)
	s = s.move(-1) // on "a", the oldest
	s, added := s.prepend([]loki.Entry{entry(8, "api", "x"), entry(9, "api", "y"), entry(10, "api", "a")})
	if added != 2 {
		t.Fatalf("added = %d, want the line already held left out", added)
	}
	if got, want := lines(s), []string{"x", "y", "a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	if r, _ := s.selected(); r.entry.Line != "a" {
		t.Errorf("cursor on %q, want it to stay on a", r.entry.Line)
	}
}

func TestTailScrollWithWrap(t *testing.T) {
	setLayout(100, 20) // 12 lines of content
	defer setLayout(142, 33)
	s := newTailState(1000)
	s.wrap = true
	long := strings.Repeat("x", 300)
	var es []loki.Entry
	for i := range 6 {
		es = append(es, entry(i, "api", long))
	}
	s = s.add(es, base)
	height := s.height(0)
	if height < 2 {
		t.Fatalf("a long line should wrap, height %d", height)
	}
	used := 0
	for i := s.offset; i <= s.cursor; i++ {
		used += s.height(i)
	}
	if used > tailBodyHeight() {
		t.Errorf("rows offset..cursor take %d lines, more than the %d on screen", used, tailBodyHeight())
	}
	out := strings.Split(s.render(true), "\n")
	if len(out) != tailBodyHeight() {
		t.Errorf("render gives %d lines, want the screen filled with %d", len(out), tailBodyHeight())
	}
	for _, l := range out {
		if w := ansi.StringWidth(l); w > contentWidth {
			t.Fatalf("line %d wide, content is %d", w, contentWidth)
		}
	}
}

func TestRateMeter(t *testing.T) {
	var r rateMeter
	now := base.Add(time.Minute)
	for i := 1; i <= 10; i++ {
		r.add(5, now.Add(-time.Duration(i)*time.Second))
	}
	r.add(100, now.Add(-time.Hour)) // too old to count
	r.add(7, now)                   // the current second is not complete yet
	if got := r.perSecond(now); got != 5 {
		t.Errorf("perSecond = %v, want 5", got)
	}
}

func TestFormat(t *testing.T) {
	tests := []struct{ got, want string }{
		{formatRate(0), "0/s"},
		{formatRate(2.345), "2.3/s"},
		{formatRate(1234.4), "1,234/s"},
		{formatSince(time.Hour), "1h"},
		{formatSince(15 * time.Minute), "15m"},
		{formatSince(90 * time.Minute), "1h30m"},
		{formatSince(48 * time.Hour), "2d"},
		{compactCount(12_345_678), "12.3M"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
}
