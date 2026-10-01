// Command seed pushes made up logs of a few services into a Loki for development: an hour of
// history, then a steady stream of new lines until it is stopped.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"time"
)

type stream struct {
	labels map[string]string
	line   func(r *rand.Rand, t time.Time) (string, map[string]string)
	weight int
}

var (
	paths   = []string{"/api/orders", "/api/orders/{id}", "/api/customers", "/api/payments", "/healthz", "/api/search"}
	methods = []string{"GET", "GET", "GET", "POST", "PUT", "DELETE"}
	users   = []string{"cus_0042", "cus_1187", "cus_2210", "cus_3301", "cus_4419"}
	jobs    = []string{"send-invoice", "sync-inventory", "rebuild-search-index", "expire-carts", "charge-subscription"}
)

func pick[T any](r *rand.Rand, s []T) T { return s[r.IntN(len(s))] }

func hex(r *rand.Rand, n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[r.IntN(len(digits))]
	}
	return string(b)
}

func level(r *rand.Rand) string {
	switch n := r.IntN(100); {
	case n < 2:
		return "error"
	case n < 8:
		return "warn"
	case n < 20:
		return "debug"
	}
	return "info"
}

// api writes JSON with the trace id as structured metadata.
func api(r *rand.Rand, t time.Time) (string, map[string]string) {
	lvl := level(r)
	status := 200
	msg := "request served"
	switch lvl {
	case "error":
		status, msg = pick(r, []int{500, 502, 503}), pick(r, []string{"upstream timed out", "database connection refused", "payment provider unavailable"})
	case "warn":
		status, msg = pick(r, []int{404, 409, 429}), pick(r, []string{"rate limit close", "order not found", "conflicting update"})
	case "debug":
		msg = "cache lookup"
	}
	trace := hex(r, 32)
	line, _ := json.Marshal(map[string]any{
		"ts":          t.UTC().Format(time.RFC3339Nano),
		"level":       lvl,
		"msg":         msg,
		"method":      pick(r, methods),
		"path":        pick(r, paths),
		"status":      status,
		"duration_ms": r.IntN(900) + 3,
		"customer":    pick(r, users),
		"trace_id":    trace,
	})
	return string(line), map[string]string{"trace_id": trace}
}

// web writes logfmt.
func web(r *rand.Rand, t time.Time) (string, map[string]string) {
	lvl := level(r)
	msg := pick(r, []string{"rendered page", "served asset", "session refreshed"})
	if lvl == "error" {
		msg = "template render failed"
	}
	return fmt.Sprintf(`ts=%s level=%s msg=%q path=%s status=%d bytes=%d`,
		t.UTC().Format(time.RFC3339), lvl, msg, pick(r, []string{"/", "/checkout", "/cart", "/static/app.js", "/account"}),
		pick(r, []int{200, 200, 200, 304, 404}), r.IntN(90000)+200), nil
}

// worker writes plain text with the level in brackets.
func worker(r *rand.Rand, t time.Time) (string, map[string]string) {
	lvl := strings.ToUpper(level(r))
	job := pick(r, jobs)
	id := hex(r, 8)
	switch lvl {
	case "ERROR":
		return fmt.Sprintf("%s [%s] job %s (%s) failed: %s, retrying in %ds", t.Format("2006-01-02 15:04:05"), lvl, job, id,
			pick(r, []string{"deadline exceeded", "connection reset by peer", "lock not acquired"}), r.IntN(60)+5), nil
	case "WARN":
		return fmt.Sprintf("%s [%s] job %s (%s) took %dms, over its budget", t.Format("2006-01-02 15:04:05"), lvl, job, id, r.IntN(9000)+2000), nil
	}
	return fmt.Sprintf("%s [%s] job %s (%s) done in %dms", t.Format("2006-01-02 15:04:05"), lvl, job, id, r.IntN(1500)+20), nil
}

// ingress writes access logs in the combined format.
func ingress(r *rand.Rand, t time.Time) (string, map[string]string) {
	return fmt.Sprintf(`10.0.%d.%d - - [%s] "%s %s HTTP/1.1" %d %d "-" "%s"`,
		r.IntN(255), r.IntN(255), t.Format("02/Jan/2006:15:04:05 -0700"), pick(r, methods), pick(r, paths),
		pick(r, []int{200, 200, 200, 201, 301, 404, 499, 502}), r.IntN(20000),
		pick(r, []string{"Mozilla/5.0", "curl/8.9.1", "kube-probe/1.31", "Go-http-client/2.0"})), nil
}

func streams() []stream {
	var out []stream
	add := func(cluster, namespace, app string, n, weight int, fn func(*rand.Rand, time.Time) (string, map[string]string)) {
		for i := range n {
			out = append(out, stream{
				labels: map[string]string{
					"cluster": cluster, "namespace": namespace, "app": app,
					"pod": fmt.Sprintf("%s-%s-%d", app, []string{"7f9c4", "5d8b2", "c41e9"}[i%3], i), "service_name": app,
				},
				line:   fn,
				weight: weight,
			})
		}
	}
	for _, cluster := range []string{"eu-west-1", "us-east-1"} {
		add(cluster, "prod", "api", 3, 6, api)
		add(cluster, "prod", "web", 2, 4, web)
		add(cluster, "prod", "worker", 1, 2, worker)
		add(cluster, "ingress", "ingress-nginx", 1, 5, ingress)
	}
	add("eu-west-1", "staging", "api", 1, 2, api)
	add("eu-west-1", "staging", "worker", 1, 1, worker)
	return out
}

type pushStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]any           `json:"values"`
}

func push(ctx context.Context, addr string, batch map[int]*pushStream) error {
	body := struct {
		Streams []*pushStream `json:"streams"`
	}{}
	for _, s := range batch {
		body.Streams = append(body.Streams, s)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(addr, "/")+"/loki/api/v1/push", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return fmt.Errorf("push: %s: %s", resp.Status, strings.TrimSpace(b.String()))
	}
	return nil
}

// generate makes n lines spread over [from, to), each in a stream picked by weight.
func generate(r *rand.Rand, all []stream, n int, from, to time.Time) map[int]*pushStream {
	total := 0
	for _, s := range all {
		total += s.weight
	}
	batch := map[int]*pushStream{}
	span := to.Sub(from)
	times := make([]time.Time, n)
	for i := range times {
		times[i] = from.Add(time.Duration(r.Int64N(int64(max(span, 1)))))
	}
	// Loki wants the entries of a stream in order.
	slices.SortFunc(times, time.Time.Compare)
	for _, t := range times {
		w := r.IntN(total)
		i := 0
		for ; w >= all[i].weight; i++ {
			w -= all[i].weight
		}
		line, meta := all[i].line(r, t)
		value := []any{strconv.FormatInt(t.UnixNano(), 10), line}
		if meta != nil {
			value = append(value, meta)
		}
		s, ok := batch[i]
		if !ok {
			s = &pushStream{Stream: all[i].labels}
			batch[i] = s
		}
		s.Values = append(s.Values, value)
	}
	return batch
}

func main() {
	addr := flag.String("addr", envOr("LOKI_ADDR", "http://localhost:3100"), "loki to push to (env LOKI_ADDR)")
	rate := flag.Int("rate", 20, "lines per second")
	backfill := flag.Duration("backfill", time.Hour, "history to push first, 0 for none")
	once := flag.Bool("once", false, "push the history and stop")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	r := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 7))
	all := streams()

	if *backfill > 0 {
		now := time.Now()
		const step = 5 * time.Minute
		perStep := max(1, *rate/10) * int(step/time.Second) / 10
		for from := now.Add(-*backfill); from.Before(now); from = from.Add(step) {
			to := from.Add(step)
			if to.After(now) {
				to = now
			}
			if err := push(ctx, *addr, generate(r, all, perStep, from, to)); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		fmt.Printf("pushed %s of history\n", *backfill)
	}
	if *once {
		return
	}

	fmt.Printf("pushing %d lines per second to %s, ctrl+c stops\n", *rate, *addr)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			// Bursts now and then, so the rate in the header moves.
			n := *rate / 4
			if r.IntN(40) == 0 {
				n *= 8
			}
			if err := push(ctx, *addr, generate(r, all, max(n, 1), last, now)); err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, err)
			}
			last = now
		}
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
