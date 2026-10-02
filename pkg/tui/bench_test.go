package tui

import (
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/stdout/pkg/loki"
)

// benchEntries makes n JSON lines spread over streams streams, a second apart.
func benchEntries(n, streams int) []loki.Entry {
	labels := make([]loki.Labels, streams)
	for i := range labels {
		labels[i] = loki.Labels{
			"app": "api", "cluster": []string{"eu-west-1", "us-east-1"}[i%2], "namespace": "prod",
			"pod": fmt.Sprintf("api-7f9c4-%d", i), "service_name": "api",
		}
	}
	out := make([]loki.Entry, n)
	for i := range out {
		out[i] = loki.Entry{
			Time:   base.Add(time.Duration(i) * time.Millisecond),
			Labels: labels[i%streams],
			Stream: labels[i%streams].String(),
			Line: fmt.Sprintf(`{"customer":"cus_%04d","duration_ms":%d,"level":"info","method":"GET","msg":"request served",`+
				`"path":"/api/orders","status":200,"trace_id":"9a9990ed9176204a1e7702f1a33b73a0","ts":"2026-10-01T19:24:56.532180962Z"}`, i%5000, i%900),
		}
	}
	return out
}

// BenchmarkTailIngest takes in batches of 50 lines into a full buffer, as a busy tail does. With
// late lines, every batch also holds lines from before the newest one in the buffer, as streams
// pushed by different agents do.
func BenchmarkTailIngest(b *testing.B) {
	for _, bench := range []struct {
		late    bool
		streams int
	}{{false, 40}, {true, 40}, {false, 2000}} {
		b.Run(fmt.Sprintf("late=%v/streams=%d", bench.late, bench.streams), func(b *testing.B) {
			defer setLayout(140, 40)
			setLayout(160, 45)
			late := bench.late
			s := newTailState(10000)
			s = s.add(benchEntries(10000, bench.streams), base)
			batch := benchEntries(50, bench.streams)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				next := make([]loki.Entry, len(batch))
				for j, e := range batch {
					// Past the 10s the buffer starts with, a millisecond per line.
					e.Time = base.Add(10*time.Second + time.Duration(i*50+j)*time.Millisecond)
					if late && j%10 == 0 {
						e.Time = e.Time.Add(-2 * time.Second)
					}
					next[j] = e
				}
				s = s.add(next, base)
			}
		})
	}
}

// BenchmarkTailRender draws a frame of a full tail.
func BenchmarkTailRender(b *testing.B) {
	for _, wrap := range []bool{false, true} {
		b.Run(fmt.Sprintf("wrap=%v", wrap), func(b *testing.B) {
			defer setLayout(140, 40)
			m := newModel("kontrolplane", "stdout", Config{Since: time.Hour, Limit: 100, Buffer: 10000})
			m.page = tailView
			m.loading = false
			m = update(&testing.T{}, m, tea.WindowSizeMsg{Width: 160, Height: 45})
			m.tail.wrap = wrap
			m.tail = m.tail.add(benchEntries(10000, 40), base)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = m.render()
			}
		})
	}
}
