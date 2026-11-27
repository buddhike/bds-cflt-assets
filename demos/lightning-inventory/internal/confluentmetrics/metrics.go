// Package confluentmetrics reads the Lightning Table usage metrics Confluent publishes
// in its Metrics API (https://docs.confluent.io/cloud/current/lightning-tables/monitor.html).
// They report compute (CCU minutes), bytes scanned by queries and storage, not latency, and
// arrive minutes late, so the UI shows them apart from the latency charts.
package confluentmetrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"lightninginv/internal/protocol"
)

// The documented metric names.
const (
	CCUMinutes   = "io.confluent.rtce/compute_utilization/ccu_minutes"
	ScannedBytes = "io.confluent.rtce/compute_utilization/dp_scanned_bytes"
	StorageBytes = "io.confluent.rtce/storage_utilization/storage_bytes"
)

// ErrRateLimited is a 429 from the Metrics API.
var ErrRateLimited = errors.New("the Metrics API rate-limited the request (429)")

// Usage is what the table used over the last hour.
type Usage struct {
	// CCUMinutes and ScannedBytes are sums over the last hour.
	CCUMinutes   float64 `json:"ccu_minutes"`
	ScannedBytes float64 `json:"scanned_bytes"`
	// StorageBytes is the latest sample.
	StorageBytes float64 `json:"storage_bytes"`
	// AsOf is the timestamp of the newest data point, unix seconds; 0 when there is none.
	AsOf int64 `json:"as_of"`
}

// Client queries the Metrics API.
type Client struct {
	URL       string // https://api.telemetry.confluent.cloud/v2/metrics/cloud/query
	Key       string
	Secret    string
	ClusterID string
	Topic     string
	HTTP      *http.Client
}

// New returns a Client for the real API.
func New(key, secret, cluster, topic string, hc *http.Client) *Client {
	return &Client{URL: "https://api.telemetry.confluent.cloud/v2/metrics/cloud/query", Key: key, Secret: secret, ClusterID: cluster, Topic: topic, HTTP: hc}
}

type point struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value"`
	Topic     string  `json:"metric.topic"`
}

func (c *Client) query(ctx context.Context, metric string, from, to time.Time) ([]point, error) {
	body, _ := json.Marshal(map[string]any{
		"aggregations": []map[string]string{{"metric": metric}},
		"filter":       map[string]string{"field": "resource.kafka.id", "op": "EQ", "value": c.ClusterID},
		"granularity":  "PT1M",
		"group_by":     []string{"metric.topic"},
		"intervals":    []string{from.UTC().Format(time.RFC3339) + "/" + to.UTC().Format(time.RFC3339)},
	})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", protocol.UserAgent)
	req.SetBasicAuth(c.Key, c.Secret)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w for %s", ErrRateLimited, metric)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the Metrics API answered %d for %s", resp.StatusCode, metric)
	}
	var out struct {
		Data []point `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Usage returns the table's usage over the hour before now.
func (c *Client) Usage(ctx context.Context, now time.Time) (Usage, error) {
	var u Usage
	from := now.Add(-time.Hour).Truncate(time.Minute)
	for _, m := range []string{CCUMinutes, ScannedBytes, StorageBytes} {
		pts, err := c.query(ctx, m, from, now)
		if err != nil {
			return Usage{}, err
		}
		var latest int64
		for _, p := range pts {
			if p.Topic != "" && p.Topic != c.Topic {
				continue
			}
			ts, err := time.Parse(time.RFC3339, p.Timestamp)
			if err != nil {
				continue
			}
			switch m {
			case CCUMinutes:
				u.CCUMinutes += p.Value
			case ScannedBytes:
				u.ScannedBytes += p.Value
			case StorageBytes:
				if ts.Unix() >= latest {
					u.StorageBytes = p.Value
				}
			}
			latest = max(latest, ts.Unix())
			u.AsOf = max(u.AsOf, ts.Unix())
		}
	}
	return u, nil
}
