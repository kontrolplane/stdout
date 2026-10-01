package loki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, srv.Client(), http.Header{"X-Scope-Orgid": {"tenant-a"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewClient(t *testing.T) {
	for _, addr := range []string{"localhost:3100", "ftp://loki", "http://", "::"} {
		if _, err := NewClient(addr, nil, nil); err == nil {
			t.Errorf("NewClient(%q) succeeded, want an error", addr)
		}
	}
	c, err := NewClient("https://user:secret@logs.example.com/loki/", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Addr(); got != "https://logs.example.com/loki" {
		t.Errorf("Addr() = %q", got)
	}
	if got := c.endpoint("/loki/api/v1/labels", nil).Path; got != "/loki/loki/api/v1/labels" {
		t.Errorf("endpoint keeps the path prefix, got %q", got)
	}
}

func TestLabelsAndValues(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/labels", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Scope-OrgID") != "tenant-a" {
			http.Error(w, "no org id", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("start") == "" || r.URL.Query().Get("end") == "" {
			http.Error(w, "missing range", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprint(w, `{"status":"success","data":["namespace","app","__name__",""]}`)
	})
	mux.HandleFunc("/loki/api/v1/label/app/values", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("query"); got != `{namespace="prod"}` {
			http.Error(w, "unexpected query "+got, http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprint(w, `{"status":"success","data":["web","api"]}`)
	})
	c := newTestClient(t, mux)
	ctx := context.Background()
	end := time.Now()

	names, err := c.Labels(ctx, "", end.Add(-time.Hour), end)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"app", "namespace"}; !slices.Equal(names, want) {
		t.Errorf("Labels() = %v, want %v", names, want)
	}
	values, err := c.LabelValues(ctx, "app", `{namespace="prod"}`, end.Add(-time.Hour), end)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"api", "web"}; !slices.Equal(values, want) {
		t.Errorf("LabelValues() = %v, want %v", values, want)
	}
}

func TestStatusErrors(t *testing.T) {
	tests := []struct {
		code int
		body string
		want string
	}{
		{http.StatusBadRequest, "parse error at line 1, col 1: syntax error: unexpected IDENTIFIER\n", "parse error at line 1, col 1: syntax error: unexpected IDENTIFIER"},
		{http.StatusUnauthorized, "no org id", "401 unauthorized: no org id"},
		{http.StatusForbidden, "", "403 forbidden: access denied"},
		{http.StatusBadGateway, "<html><body>bad gateway</body></html>", "502 bad gateway"},
		{http.StatusTooManyRequests, `{"message":"slow down"}`, "slow down"},
	}
	for _, tt := range tests {
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.code)
			_, _ = fmt.Fprint(w, tt.body)
		}))
		_, err := c.Labels(context.Background(), "", time.Time{}, time.Time{})
		if err == nil || err.Error() != tt.want {
			t.Errorf("status %d: err = %v, want %q", tt.code, err, tt.want)
		}
		if !IsStatus(err, tt.code) {
			t.Errorf("status %d: IsStatus = false", tt.code)
		}
	}
}

const streamsBody = `{"status":"success","data":{"resultType":"streams","result":[
	{"stream":{"app":"api"},"values":[["3000","third",{"structuredMetadata":{"trace_id":"abc"},"parsed":{"level":"warn"}}],["1000","first"]]},
	{"stream":{"app":"web"},"values":[["2000","second"]]}
]}}`

func TestQueryRange(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/loki/api/v1/query_range" || q.Get("direction") != "backward" || q.Get("limit") != "3" {
			http.Error(w, "unexpected request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-Loki-Response-Encoding-Flags") != "categorize-labels" {
			http.Error(w, "labels not categorized", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprint(w, streamsBody)
	}))
	entries, err := c.QueryRange(context.Background(), `{app=~".+"}`, time.Unix(0, 0), time.Unix(0, 5000), 3, true)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range entries {
		lines = append(lines, e.Line)
	}
	if want := []string{"first", "second", "third"}; !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	third := entries[2]
	if third.Metadata["trace_id"] != "abc" || third.Parsed["level"] != "warn" || third.Labels["app"] != "api" {
		t.Errorf("categorized labels not decoded: %+v", third)
	}
	if DetectLevel(third) != LevelWarn {
		t.Errorf("DetectLevel = %v, want warn from the parsed labels", DetectLevel(third))
	}
}

func TestQueryRangeRefusesMetricQueries(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	_, err := c.QueryRange(context.Background(), `rate({app="api"}[1m])`, time.Time{}, time.Time{}, 10, true)
	if err == nil {
		t.Fatal("want an error for a metric query")
	}
}

func TestVolume(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("targetLabels") != "app" || r.URL.Query().Get("aggregateBy") != "series" {
			http.Error(w, "no target", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"app":"api"},"value":[1700000000,"2048"]},
			{"metric":{"app":"web"},"value":[1700000000,"512"]}]}}`)
	}))
	vol, err := c.Volume(context.Background(), `{app=~".+"}`, "app", time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if vol["api"] != 2048 || vol["web"] != 512 {
		t.Errorf("Volume() = %v", vol)
	}
}

// tailServer serves the tail endpoint: every connection gets the next script of messages, after
// which the server closes it.
type tailServer struct {
	mu      sync.Mutex
	scripts [][]string
	starts  []string
	limits  []string
}

func (s *tailServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/loki/api/v1/tail" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("query") == "bad" {
		http.Error(w, "parse error at line 1, col 1: syntax error", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.starts = append(s.starts, r.URL.Query().Get("start"))
	s.limits = append(s.limits, r.URL.Query().Get("limit"))
	var script []string
	if len(s.scripts) > 0 {
		script, s.scripts = s.scripts[0], s.scripts[1:]
	}
	s.mu.Unlock()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx := r.Context()
	for _, msg := range script {
		if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			return
		}
	}
	if r.URL.Query().Get("query") == "metric" {
		_ = conn.Close(websocket.StatusInternalError, "only log selector is supported")
		return
	}
	if script == nil {
		<-ctx.Done()
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func tailMsg(ts int64, app, line string) string {
	b, _ := json.Marshal(map[string]any{
		"streams": []any{map[string]any{
			"stream": map[string]string{"app": app},
			"values": [][]string{{strconv.FormatInt(ts, 10), line}},
		}},
	})
	return string(b)
}

func TestTail(t *testing.T) {
	srv := &tailServer{scripts: [][]string{{
		tailMsg(1000, "api", "one"),
		`{"streams":[],"dropped_entries":[{"labels":{"app":"api"},"timestamp":"1500"}]}`,
	}}}
	c := newTestClient(t, srv)
	var batches []TailBatch
	opened := false
	err := c.Tail(context.Background(), `{app="api"}`, time.Unix(0, 100), 10, func() { opened = true }, func(b TailBatch) {
		batches = append(batches, b)
	})
	if !errors.Is(err, ErrTailClosed) {
		t.Fatalf("err = %v, want ErrTailClosed", err)
	}
	if !opened {
		t.Error("opened was not called")
	}
	if len(batches) != 2 || batches[0].Entries[0].Line != "one" || len(batches[1].Dropped) != 1 {
		t.Fatalf("batches = %+v", batches)
	}
	if srv.starts[0] != "100" || srv.limits[0] != "10" {
		t.Errorf("start, limit = %s, %s", srv.starts[0], srv.limits[0])
	}
}

func TestTailRefused(t *testing.T) {
	c := newTestClient(t, &tailServer{})
	err := c.Tail(context.Background(), "bad", time.Time{}, 10, nil, func(TailBatch) {})
	if !IsStatus(err, http.StatusBadRequest) {
		t.Fatalf("err = %v, want a 400", err)
	}
	if err.Error() != "parse error at line 1, col 1: syntax error" {
		t.Errorf("err = %q", err)
	}
}

func TestFollowResumesWithoutRepeats(t *testing.T) {
	srv := &tailServer{scripts: [][]string{
		{tailMsg(1000, "api", "one"), tailMsg(2000, "api", "two"), tailMsg(2000, "web", "two")},
		// Resumed at 2000, the server sends the entries at 2000 again.
		{tailMsg(2000, "api", "two"), tailMsg(2000, "web", "two"), tailMsg(3000, "api", "three")},
	}}
	c := newTestClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan FollowEvent)
	go c.Follow(ctx, `{app=~".+"}`, time.Time{}, 100, events)

	var lines []string
	var states []FollowState
	timeout := time.After(10 * time.Second)
	for len(lines) < 4 {
		select {
		case e := <-events:
			states = append(states, e.State)
			for _, entry := range e.Batch.Entries {
				lines = append(lines, entry.Labels["app"]+":"+entry.Line)
			}
		case <-timeout:
			t.Fatalf("timed out, lines so far %v", lines)
		}
	}
	if want := []string{"api:one", "api:two", "web:two", "api:three"}; !slices.Equal(lines, want) {
		t.Errorf("lines = %v, want %v", lines, want)
	}
	if !slices.Contains(states, Retrying) {
		t.Errorf("states = %v, want a retry when the server closed the tail", states)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.starts) < 2 || srv.starts[1] != "2000" || srv.limits[1] != strconv.Itoa(resumeLimit) {
		t.Errorf("resumed with start %v limit %v", srv.starts, srv.limits)
	}
}

func TestFollowStopsOnRefusal(t *testing.T) {
	c := newTestClient(t, &tailServer{})
	events := make(chan FollowEvent)
	go c.Follow(context.Background(), "bad", time.Time{}, 100, events)
	var last FollowEvent
	for e := range events {
		last = e
	}
	if last.State != Failed || !IsStatus(last.Err, http.StatusBadRequest) {
		t.Errorf("last event = %+v, want Failed with the 400", last)
	}
}

func TestFollowStopsWhenClosedWithAReason(t *testing.T) {
	c := newTestClient(t, &tailServer{})
	events := make(chan FollowEvent)
	go c.Follow(context.Background(), "metric", time.Time{}, 100, events)
	var last FollowEvent
	for e := range events {
		last = e
	}
	var refused *RefusedError
	if last.State != Failed || !errors.As(last.Err, &refused) || refused.Reason != "only log selector is supported" {
		t.Errorf("last event = %+v, want Failed with the reason", last)
	}
}
