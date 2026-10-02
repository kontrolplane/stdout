package loki

import (
	"slices"
	"testing"
	"time"
)

func TestMatcher(t *testing.T) {
	tests := []struct {
		m    Matcher
		want string
	}{
		{Matcher{Label: "app"}, ""},
		{Matcher{Label: "app", Values: []string{"api"}}, `app="api"`},
		{Matcher{Label: "app", Values: []string{"api"}, Exclude: true}, `app!="api"`},
		{Matcher{Label: "app", Values: []string{"web", "api"}}, `app=~"api|web"`},
		{Matcher{Label: "path", Values: []string{"/v1/a.b", "c+d"}, Exclude: true}, `path!~"/v1/a\\.b|c\\+d"`},
		{Matcher{Label: "msg", Values: []string{`say "hi"`}}, `msg="say \"hi\""`},
	}
	for _, tt := range tests {
		if got := tt.m.String(); got != tt.want {
			t.Errorf("%+v: got %s, want %s", tt.m, got, tt.want)
		}
	}
}

func TestSelector(t *testing.T) {
	got := Selector(
		Matcher{Label: "namespace", Values: []string{"prod"}},
		Matcher{Label: "pod"},
		Matcher{Label: "app", Values: []string{"api", "web"}, Exclude: true},
	)
	if want := `{namespace="prod", app!~"api|web"}`; got != want {
		t.Errorf("Selector() = %s, want %s", got, want)
	}
	if Selector(Matcher{Label: "app"}) != "" {
		t.Error("a selector without values should be empty")
	}
	if Selects(Matcher{Label: "app", Values: []string{"api"}, Exclude: true}) {
		t.Error("only excluding matchers do not select streams")
	}
	if !Selects(Matcher{Label: "app", Values: []string{"api"}}) {
		t.Error("an including matcher selects streams")
	}
	if got := StreamSelector(Labels{"b": "2", "a": "1"}); got != `{a="1", b="2"}` {
		t.Errorf("StreamSelector() = %s", got)
	}
	if got := WithLineFilter(`{a="1"}`, `timeout "x"`); got != `{a="1"} |= "timeout \"x\""` {
		t.Errorf("WithLineFilter() = %s", got)
	}
}

func TestDetectLevel(t *testing.T) {
	tests := []struct {
		e    Entry
		want Level
	}{
		{Entry{Labels: Labels{"level": "WARNING"}}, LevelWarn},
		{Entry{Labels: Labels{"detected_level": "error"}}, LevelError},
		{Entry{Metadata: Labels{"severity": "critical"}}, LevelFatal},
		{Entry{Line: `{"lvl":"debug","msg":"x"}`}, LevelDebug},
		{Entry{Line: `{"level":30,"msg":"pino"}`}, LevelInfo},
		{Entry{Line: `{"severity":"warning","nested":{"level":"debug"},"Level":"error"}`}, LevelError},
		{Entry{Line: `{"msg":"has \"level\":\"error\" in it","lvl":"info"}`}, LevelInfo},
		{Entry{Line: `{"msg":"[WARN] in a field"}`}, LevelWarn},
		{Entry{Line: `{"level":"error"`}, LevelUnknown},
		{Entry{Line: `ts=2026-10-01 level=error msg="boom"`}, LevelError},
		{Entry{Line: `2026-10-01T12:00:00Z [WARN] disk almost full`}, LevelWarn},
		{Entry{Line: `E1001 12:00:00 klog style`}, LevelUnknown},
		{Entry{Line: `user info updated`}, LevelUnknown},
		{Entry{Line: `GET /healthz 200`}, LevelUnknown},
	}
	for _, tt := range tests {
		if got := DetectLevel(tt.e); got != tt.want {
			t.Errorf("DetectLevel(%+v) = %v, want %v", tt.e, got, tt.want)
		}
	}
}

func TestFields(t *testing.T) {
	json := Fields(`{"msg":"hello","n":1.5,"user":{"id":7},"ok":true}`)
	want := []Field{{"msg", "hello"}, {"n", "1.5"}, {"user", `{"id":7}`}, {"ok", "true"}}
	if !slices.Equal(json, want) {
		t.Errorf("json fields = %v, want %v", json, want)
	}
	lf := Fields(`level=info msg="request done" path=/api status=200`)
	want = []Field{{"level", "info"}, {"msg", "request done"}, {"path", "/api"}, {"status", "200"}}
	if !slices.Equal(lf, want) {
		t.Errorf("logfmt fields = %v, want %v", lf, want)
	}
	if f := Fields(`the answer is x=42 according to the docs`); f != nil {
		t.Errorf("prose taken for logfmt: %v", f)
	}
	if Format(`{"a":1}`) != "json" || Format(`a=1 b=2`) != "logfmt" || Format("plain") != "text" {
		t.Error("Format misnames a line")
	}
}

func TestPretty(t *testing.T) {
	got, ok := Pretty(`{"a":{"b":1}}`)
	if !ok || got != "{\n  \"a\": {\n    \"b\": 1\n  }\n}" {
		t.Errorf("Pretty() = %q, %v", got, ok)
	}
	if got, ok := Pretty("not json"); ok || got != "not json" {
		t.Errorf("Pretty() changed text: %q", got)
	}
}

func TestVaryingLabels(t *testing.T) {
	streams := []Labels{
		{"cluster": "eu", "app": "api", "pod": "api-1"},
		{"cluster": "eu", "app": "api", "pod": "api-2"},
		{"cluster": "eu", "app": "web", "pod": "web-1", "canary": "true"},
	}
	if got, want := VaryingLabels(streams), []string{"app", "canary", "pod"}; !slices.Equal(got, want) {
		t.Errorf("VaryingLabels() = %v, want %v", got, want)
	}
	for _, s := range streams {
		s["service_name"] = s["app"]
	}
	if got, want := VaryingLabels(streams), []string{"app", "canary", "pod"}; !slices.Equal(got, want) {
		t.Errorf("a label mirroring another should be left out, got %v", got)
	}
	if VaryingLabels(streams[:1]) != nil {
		t.Error("a single stream has no varying labels")
	}
}

func TestDedupe(t *testing.T) {
	at := func(n int64, line string) Entry {
		return Entry{Time: time.Unix(0, n), Line: line, Labels: Labels{"a": "1"}}
	}
	var d dedupe
	first := d.filter([]Entry{at(1, "a"), at(2, "b")})
	if len(first) != 2 {
		t.Fatalf("first = %v", first)
	}
	again := d.filter([]Entry{at(2, "b"), at(2, "c"), at(1, "late"), at(3, "d")})
	var lines []string
	for _, e := range again {
		lines = append(lines, e.Line)
	}
	if want := []string{"c", "late", "d"}; !slices.Equal(lines, want) {
		t.Errorf("lines = %v, want %v", lines, want)
	}
}
