// Package vm is a small client for VictoriaMetrics's Prometheus-compatible
// HTTP API — instant queries, range queries, and raw export for the data
// export feature.
package vm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

type Point struct {
	Time  float64
	Value float64
}

type queryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func parseFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	default:
		return 0
	}
}

func (c *Client) do(path string, params url.Values) (queryResponse, error) {
	u := c.BaseURL + path + "?" + params.Encode()
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return queryResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return queryResponse{}, err
	}
	if resp.StatusCode != 200 {
		return queryResponse{}, fmt.Errorf("vm query failed: %s: %s", resp.Status, string(body))
	}
	var qr queryResponse
	if err := json.Unmarshal(body, &qr); err != nil {
		return queryResponse{}, err
	}
	return qr, nil
}

// InstantQuery returns the current value of a PromQL expression, or
// (0, false) if there's no data (e.g. the metric doesn't exist yet).
func (c *Client) InstantQuery(promql string) (float64, bool, error) {
	qr, err := c.do("/api/v1/query", url.Values{"query": {promql}})
	if err != nil {
		return 0, false, err
	}
	if len(qr.Data.Result) == 0 {
		return 0, false, nil
	}
	return parseFloat(qr.Data.Result[0].Value[1]), true, nil
}

// RangeQuery returns a time series for a PromQL expression between start
// and end (unix seconds), evaluated every step.
func (c *Client) RangeQuery(promql string, start, end time.Time, step time.Duration) ([]Point, error) {
	qr, err := c.do("/api/v1/query_range", url.Values{
		"query": {promql},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {fmt.Sprintf("%ds", int(step.Seconds()))},
	})
	if err != nil {
		return nil, err
	}
	if len(qr.Data.Result) == 0 {
		return nil, nil
	}
	pts := make([]Point, 0, len(qr.Data.Result[0].Values))
	for _, v := range qr.Data.Result[0].Values {
		pts = append(pts, Point{Time: parseFloat(v[0]), Value: parseFloat(v[1])})
	}
	return pts, nil
}

// ExportRaw streams VictoriaMetrics's native JSON-lines export for a
// selector between start and end — used by the export feature to hand back
// raw data rather than an aggregated query result.
func (c *Client) ExportRaw(matchSelector string, start, end time.Time) (io.ReadCloser, error) {
	u := c.BaseURL + "/api/v1/export?" + url.Values{
		"match": {matchSelector},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
	}.Encode()
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("export failed: %s: %s", resp.Status, string(body))
	}
	return resp.Body, nil
}
