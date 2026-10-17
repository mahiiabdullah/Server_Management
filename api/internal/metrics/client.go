// Package metrics talks to Prometheus over its HTTP API and turns PromQL results
// into the values the dashboard shows. Every query is bounded by a timeout.
package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// ErrUnavailable wraps every failure to reach or query Prometheus.
var ErrUnavailable = errors.New("prometheus unavailable")

// Ready reports whether Prometheus can serve queries.
func (c *Client) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/-/ready", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("prometheus not ready: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Sample is one instant-vector element.
type Sample struct {
	Labels map[string]string
	Time   time.Time
	Value  float64
}

// Series is one range-vector element.
type Series struct {
	Labels map[string]string
	Points []Point
}

type Point struct {
	Time  time.Time
	Value float64
}

type apiResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

func (c *Client) get(ctx context.Context, path string, params url.Values) (*apiResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var r apiResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("%w: HTTP %d, unreadable body", ErrUnavailable, resp.StatusCode)
	}
	if r.Status != "success" {
		if resp.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: %s: %s", ErrUnavailable, r.ErrorType, r.Error)
		}
		return nil, fmt.Errorf("promql %s: %s", r.ErrorType, r.Error)
	}
	return &r, nil
}

// Query runs an instant query. A zero t means "now".
func (c *Client) Query(ctx context.Context, q string, t time.Time) ([]Sample, error) {
	p := url.Values{"query": {q}}
	if !t.IsZero() {
		p.Set("time", formatTime(t))
	}
	r, err := c.get(ctx, "/api/v1/query", p)
	if err != nil {
		return nil, err
	}
	switch r.Data.ResultType {
	case "vector":
		var raw []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		if err := json.Unmarshal(r.Data.Result, &raw); err != nil {
			return nil, err
		}
		out := make([]Sample, 0, len(raw))
		for _, s := range raw {
			ts, v, ok := parsePair(s.Value)
			if !ok {
				continue
			}
			out = append(out, Sample{Labels: s.Metric, Time: ts, Value: v})
		}
		return out, nil
	case "scalar":
		var raw [2]any
		if err := json.Unmarshal(r.Data.Result, &raw); err != nil {
			return nil, err
		}
		ts, v, ok := parsePair(raw)
		if !ok {
			return nil, nil
		}
		return []Sample{{Time: ts, Value: v}}, nil
	default:
		return nil, fmt.Errorf("unexpected result type %q", r.Data.ResultType)
	}
}

// QueryRange runs a range query.
func (c *Client) QueryRange(ctx context.Context, q string, start, end time.Time, step time.Duration) ([]Series, error) {
	r, err := c.get(ctx, "/api/v1/query_range", url.Values{
		"query": {q}, "start": {formatTime(start)}, "end": {formatTime(end)},
		"step": {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	})
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Metric map[string]string `json:"metric"`
		Values [][2]any          `json:"values"`
	}
	if err := json.Unmarshal(r.Data.Result, &raw); err != nil {
		return nil, err
	}
	out := make([]Series, 0, len(raw))
	for _, s := range raw {
		pts := make([]Point, 0, len(s.Values))
		for _, pair := range s.Values {
			if ts, v, ok := parsePair(pair); ok {
				pts = append(pts, Point{ts, v})
			}
		}
		out = append(out, Series{Labels: s.Metric, Points: pts})
	}
	return out, nil
}

func formatTime(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 3, 64)
}

// parsePair decodes Prometheus' [unix_seconds, "value"] pair. NaN and ±Inf are dropped.
func parsePair(p [2]any) (time.Time, float64, bool) {
	sec, ok := p[0].(float64)
	if !ok {
		return time.Time{}, 0, false
	}
	str, ok := p[1].(string)
	if !ok {
		return time.Time{}, 0, false
	}
	v, err := strconv.ParseFloat(str, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return time.Time{}, 0, false
	}
	whole, frac := math.Modf(sec)
	return time.Unix(int64(whole), int64(frac*1e9)).UTC(), v, true
}
