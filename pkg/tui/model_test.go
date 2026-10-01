package tui

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/coder/websocket"

	"github.com/kontrolplane/stdout/pkg/client"
	"github.com/kontrolplane/stdout/pkg/loki"
	"github.com/kontrolplane/stdout/pkg/tui/commands"
	"github.com/kontrolplane/stdout/pkg/tui/messages"
)

func press(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	if ctrl, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: rune(ctrl[0]), Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// send feeds keys to the model, dropping the commands they return.
func send(t *testing.T, m model, keys ...string) model {
	t.Helper()
	for _, k := range keys {
		next, _ := m.Update(press(k))
		m = next.(model)
	}
	return m
}

func typeText(t *testing.T, m model, s string) model {
	t.Helper()
	for _, r := range s {
		next, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(model)
	}
	return m
}

func update(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(model)
}

func testConfig() Config { return Config{Since: time.Hour, Limit: 100, Buffer: 1000} }

func sized(m model) model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	return next.(model)
}

func pickerModel(t *testing.T) model {
	t.Helper()
	m := sized(newModel("kontrolplane", "stdout", testConfig()))
	m = update(t, m, messages.LabelsLoadedMsg{Labels: []string{"app", "namespace"}})
	m = update(t, m, messages.ValuesLoadedMsg{Label: "app", Values: []string{"api", "web", "worker"}, Volumes: map[string]uint64{"api": 10, "web": 30, "worker": 20}})
	return m
}

func TestPickerBuildsSelector(t *testing.T) {
	m := pickerModel(t)
	if m.loading || m.page != labelPicker {
		t.Fatalf("loading %v, page %v", m.loading, m.page)
	}
	// Values are listed by volume: web, worker, api.
	if got := m.picker.filteredValues(); strings.Join(got, ",") != "web,worker,api" {
		t.Fatalf("values = %v", got)
	}
	m = send(t, m, "tab", "space", "j", "j", "space")
	if got := m.picker.query(); got != `{app=~"api|web"}` {
		t.Fatalf("query = %s", got)
	}
	m = send(t, m, "!")
	if got := m.picker.query(); got != `{app!~"api|web"}` {
		t.Fatalf("query = %s", got)
	}
	m = send(t, m, "enter")
	if m.page != labelPicker || !strings.Contains(m.statusMsg, "only excludes") {
		t.Errorf("a selector that only excludes should be refused, page %v, status %q", m.page, m.statusMsg)
	}
	m = send(t, m, "!", "x")
	if m.picker.query() != "" {
		t.Errorf("x should clear the label, query %s", m.picker.query())
	}
}

func TestPickerScopesValues(t *testing.T) {
	m := pickerModel(t)
	m = send(t, m, "tab", "space") // app="web"
	if got := m.picker.scope("namespace"); got != `{app="web"}` {
		t.Errorf("scope of namespace = %q", got)
	}
	if got := m.picker.scope("app"); got != "" {
		t.Errorf("a label is not scoped by its own selection, got %q", got)
	}
	m.picker = m.picker.toggle("namespace", "prod")
	m.picker.selection["namespace"] = loki.Matcher{Label: "namespace", Values: []string{"prod"}, Exclude: true}
	if got := m.picker.scope("app"); got != "" {
		t.Errorf("an excluding selection cannot scope by itself, got %q", got)
	}
}

func TestPickerWithoutVolumes(t *testing.T) {
	m := sized(newModel("kontrolplane", "stdout", testConfig()))
	m = update(t, m, messages.LabelsLoadedMsg{Labels: []string{"app"}})
	m = update(t, m, messages.ValuesLoadedMsg{Label: "app", Values: []string{"b", "a"}, VolumeErr: &loki.StatusError{Code: http.StatusNotFound}})
	if !m.picker.noVolume {
		t.Error("a 404 for volumes should turn them off")
	}
	if got := m.picker.filteredValues(); strings.Join(got, ",") != "b,a" {
		t.Errorf("values keep the server's order without volumes, got %v", got)
	}
}

func TestPickerFilter(t *testing.T) {
	m := pickerModel(t)
	m = send(t, m, "/")
	m = typeText(t, m, "name")
	if got := m.picker.filteredLabels(); len(got) != 1 || got[0] != "namespace" {
		t.Fatalf("labels = %v", got)
	}
	m = send(t, m, "enter", "esc")
	if len(m.picker.filteredLabels()) != 2 {
		t.Error("esc should clear the filter")
	}
}

func tailModel(t *testing.T) model {
	t.Helper()
	m := sized(newModel("kontrolplane", "stdout", testConfig()))
	m.loading = false
	m.page = tailView
	m.tail.query = `{app="api"}`
	m.tail = m.tail.add([]loki.Entry{
		entry(1, "api", `GET /orders 200`),
		entry(2, "web", `POST /orders 500`),
		entry(3, "api", `{"level":"error","msg":"boom"}`),
	}, base)
	return m
}

func TestTailKeys(t *testing.T) {
	m := tailModel(t)
	m = send(t, m, "/")
	m = typeText(t, m, "orders")
	m = send(t, m, "enter")
	if got := lines(m.tail); len(got) != 2 {
		t.Fatalf("filtered lines = %v", got)
	}
	m = send(t, m, "space")
	if len(m.tail.pins) != 1 {
		t.Fatal("space should pin the line")
	}
	m = send(t, m, "esc")
	if m.tail.filter.active() || m.page != tailView {
		t.Fatal("esc should clear the filter before leaving")
	}
	m = send(t, m, "w", "l")
	if !m.tail.wrap || !m.tail.hideLabels {
		t.Error("w and l should toggle wrapping and the labels")
	}
	m = send(t, m, "p")
	m.tail = m.tail.add([]loki.Entry{entry(9, "api", "while paused")}, base)
	if len(m.tail.rows) != 3 {
		t.Error("lines should be held while paused")
	}
	m = send(t, m, "p")
	if len(m.tail.rows) != 4 {
		t.Errorf("resuming should show the held lines, rows %d", len(m.tail.rows))
	}
}

func TestDetailsNavigation(t *testing.T) {
	m := tailModel(t)
	m = send(t, m, "enter")
	if m.page != lineDetails || m.details.row.entry.Line != `{"level":"error","msg":"boom"}` {
		t.Fatalf("page %v, line %q", m.page, m.details.row.entry.Line)
	}
	if m.tail.follow {
		t.Error("opening a line should stop following, so the cursor stays on it")
	}
	view := ansi.Strip(m.DetailsView())
	for _, want := range []string{`"msg": "boom"`, "error", "fields"} {
		if !strings.Contains(view, want) {
			t.Errorf("details view lacks %q", want)
		}
	}
	m = send(t, m, "[")
	if m.details.row.entry.Line != "POST /orders 500" {
		t.Errorf("[ should open the previous line, got %q", m.details.row.entry.Line)
	}
	m = send(t, m, "space", "q")
	if m.page != tailView || m.tail.cursor != 1 || len(m.tail.pins) != 1 {
		t.Errorf("page %v, cursor %d, pins %d", m.page, m.tail.cursor, len(m.tail.pins))
	}
}

func TestEditor(t *testing.T) {
	m := pickerModel(t)
	m = send(t, m, "e")
	if !m.editing || m.editor.Value() != "{}" {
		t.Fatalf("editing %v, value %q", m.editing, m.editor.Value())
	}
	m = send(t, m, "enter")
	if !m.editing || !strings.Contains(m.statusMsg, "write a query") {
		t.Error("an empty selector should not be tailed")
	}
	m = send(t, m, "esc")
	if m.editing {
		t.Error("esc should close the editor")
	}
}

// fakeLoki serves labels and a tail that sends one batch and then holds the connection.
func fakeLoki(t *testing.T) *loki.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/labels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"success","data":["app"]}`)
	})
	mux.HandleFunc("/loki/api/v1/tail", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ts := time.Now().UnixNano()
		msg := fmt.Sprintf(`{"streams":[{"stream":{"app":"api"},"values":[["%d","first"],["%d","second"]]}]}`, ts, ts+1)
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(msg))
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, _, err := client.New(client.Options{Addr: srv.URL}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTailFromServer(t *testing.T) {
	c := fakeLoki(t)
	m := sized(newModel("kontrolplane", "stdout", testConfig()))
	m.client = c
	m, _ = m.startTail(`{app="api"}`)
	defer m.stopTail()
	if m.page != tailView || !m.tail.running {
		t.Fatalf("page %v, running %v", m.page, m.tail.running)
	}
	deadline := time.After(5 * time.Second)
	for len(m.tail.rows) < 2 {
		done := make(chan tea.Msg, 1)
		go func() { done <- commands.WaitTail(m.tail.events, m.tail.gen)() }()
		select {
		case msg := <-done:
			m = update(t, m, msg)
		case <-deadline:
			t.Fatalf("timed out, rows %d", len(m.tail.rows))
		}
	}
	if m.tail.state != loki.Live || lines(m.tail)[1] != "second" {
		t.Errorf("state %v, lines %v", m.tail.state, lines(m.tail))
	}

	// A message of the follow before a restart is ignored.
	old := m.tail.gen
	m, _ = m.startTail(`{app="web"}`)
	m = update(t, m, messages.TailMsg{Gen: old, Entries: []loki.Entry{entry(1, "api", "stale")}})
	if len(m.tail.rows) != 0 {
		t.Error("messages of an old follow should be ignored")
	}
}

func TestTailFailureShowsError(t *testing.T) {
	m := tailModel(t)
	m.tail.running = true
	m.tail.gen = 3
	m = update(t, m, messages.TailMsg{Gen: 3, State: loki.Failed, Err: &loki.RefusedError{Reason: "only log selector is supported"}})
	if m.tail.running || !strings.Contains(m.error, "only log selector") {
		t.Errorf("running %v, error %q", m.tail.running, m.error)
	}
	m = send(t, m, "x")
	if m.error != "" {
		t.Error("any key should dismiss the error")
	}
}

func TestOlderLines(t *testing.T) {
	m := tailModel(t)
	m = send(t, m, "g")
	m = update(t, m, messages.OlderLoadedMsg{Gen: m.tail.gen, Start: base.Add(-time.Hour), End: base, Entries: nil})
	if !strings.Contains(m.statusMsg, "no lines") || !m.tail.olderEnd.Equal(base.Add(-time.Hour)) {
		t.Errorf("status %q, older end %v", m.statusMsg, m.tail.olderEnd)
	}
	m = update(t, m, messages.OlderLoadedMsg{Gen: m.tail.gen, End: base, Entries: []loki.Entry{entry(-5, "api", "older")}})
	if lines(m.tail)[0] != "older" || m.tail.cursor != 0 {
		t.Errorf("lines %v, cursor %d", lines(m.tail), m.tail.cursor)
	}
	m = update(t, m, messages.OlderLoadedMsg{Gen: m.tail.gen, Err: errors.New("boom")})
	if !strings.Contains(m.statusMsg, "boom") {
		t.Errorf("status %q", m.statusMsg)
	}
}
