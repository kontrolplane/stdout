package loki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Client calls the API of one Loki server or gateway.
type Client struct {
	base   *url.URL
	http   *http.Client
	header http.Header
}

// NewClient returns a client for the server at addr. Every request carries header, which holds
// the authentication and the tenant. The http client carries the tls configuration.
func NewClient(addr string, httpClient *http.Client, header http.Header) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(addr, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid address %q: the scheme must be http or https", addr)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid address %q: no host", addr)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{base: u, http: httpClient, header: header.Clone()}, nil
}

// Addr is the address of the server, without credentials.
func (c *Client) Addr() string {
	u := *c.base
	u.User = nil
	return u.String()
}

// StatusError is a response the server answered with an error status.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%d %s", e.Code, strings.ToLower(http.StatusText(e.Code)))
	}
	return e.Message
}

// IsStatus reports whether err is a response with one of the given status codes.
func IsStatus(err error, codes ...int) bool {
	var s *StatusError
	return errors.As(err, &s) && slices.Contains(codes, s.Code)
}

func (c *Client) endpoint(path string, query url.Values) *url.URL {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = query.Encode()
	return &u
}

// get calls path and decodes the response into out.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(path, query).String(), nil)
	if err != nil {
		return err
	}
	for k, v := range c.header {
		req.Header[k] = v
	}
	// Asks Loki 3 to set structured metadata and parsed labels apart from the stream labels.
	req.Header.Set("X-Loki-Response-Encoding-Flags", "categorize-labels")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return statusError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding the response of %s: %w", path, err)
	}
	return nil
}

// statusError reads the error Loki answered with. Loki writes errors as plain text, a gateway may
// send a page of html, of which only the status is worth showing.
func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	if strings.HasPrefix(msg, "<") {
		msg = ""
	}
	var js struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &js) == nil {
		msg = cmpOr(js.Message, js.Error, msg)
	}
	// A bare "no org id" or "invalid token" reads better with the status in front of it.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		msg = fmt.Sprintf("%d %s: %s", resp.StatusCode, strings.ToLower(http.StatusText(resp.StatusCode)), cmpOr(msg, "access denied"))
	}
	return &StatusError{Code: resp.StatusCode, Message: msg}
}

func cmpOr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func timeRange(start, end time.Time) url.Values {
	q := url.Values{}
	if !start.IsZero() {
		q.Set("start", nanos(start))
	}
	if !end.IsZero() {
		q.Set("end", nanos(end))
	}
	return q
}

type stringsResponse struct {
	Data []string `json:"data"`
}

// Labels lists the label names seen between start and end. With a selector set, only the names
// of the streams it matches are listed; servers before Loki 3 ignore it.
func (c *Client) Labels(ctx context.Context, selector string, start, end time.Time) ([]string, error) {
	q := timeRange(start, end)
	if selector != "" {
		q.Set("query", selector)
	}
	var resp stringsResponse
	if err := c.get(ctx, "/loki/api/v1/labels", q, &resp); err != nil {
		return nil, err
	}
	names := slices.DeleteFunc(resp.Data, func(name string) bool { return name == "" || name == "__name__" })
	slices.Sort(names)
	return names, nil
}

// LabelValues lists the values of the label seen between start and end, of the streams selector
// matches, or of all streams when it is empty.
func (c *Client) LabelValues(ctx context.Context, name, selector string, start, end time.Time) ([]string, error) {
	q := timeRange(start, end)
	if selector != "" {
		q.Set("query", selector)
	}
	var resp stringsResponse
	if err := c.get(ctx, "/loki/api/v1/label/"+url.PathEscape(name)+"/values", q, &resp); err != nil {
		return nil, err
	}
	slices.Sort(resp.Data)
	return resp.Data, nil
}

// Volume reports the bytes ingested between start and end per value of the label target, for the
// streams selector matches. It needs Loki 3 with volume_enabled, other servers answer 404 or 400.
func (c *Client) Volume(ctx context.Context, selector, target string, start, end time.Time) (map[string]uint64, error) {
	q := timeRange(start, end)
	q.Set("query", selector)
	q.Set("targetLabels", target)
	// Per series, narrowed to the target label; "labels" would sum per label name instead.
	q.Set("aggregateBy", "series")
	q.Set("limit", "1000")
	var resp struct {
		Data struct {
			Result []struct {
				Metric Labels            `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/loki/api/v1/index/volume", q, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		if len(r.Value) < 2 {
			continue
		}
		var s string
		if json.Unmarshal(r.Value[1], &s) != nil {
			continue
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		out[r.Metric[target]] += uint64(n)
	}
	return out, nil
}

// QueryRange runs a log query between start and end and returns at most limit entries, the newest
// when backward is set, oldest first either way.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, limit int, backward bool) ([]Entry, error) {
	q := timeRange(start, end)
	q.Set("query", query)
	q.Set("limit", strconv.Itoa(limit))
	if backward {
		q.Set("direction", "backward")
	} else {
		q.Set("direction", "forward")
	}
	var resp struct {
		Data struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/loki/api/v1/query_range", q, &resp); err != nil {
		return nil, err
	}
	if resp.Data.ResultType != "streams" {
		return nil, fmt.Errorf("the query returns %s, only log queries can be tailed", resp.Data.ResultType)
	}
	var streams []stream
	if err := json.Unmarshal(resp.Data.Result, &streams); err != nil {
		return nil, fmt.Errorf("decoding streams: %w", err)
	}
	var entries []Entry
	for _, s := range streams {
		e, err := s.entries()
		if err != nil {
			return nil, err
		}
		entries = append(entries, e...)
	}
	sortEntries(entries)
	return entries, nil
}

// BuildInfo asks the server for its version.
func (c *Client) BuildInfo(ctx context.Context) (BuildInfo, error) {
	var info BuildInfo
	err := c.get(ctx, "/loki/api/v1/status/buildinfo", nil, &info)
	return info, err
}
