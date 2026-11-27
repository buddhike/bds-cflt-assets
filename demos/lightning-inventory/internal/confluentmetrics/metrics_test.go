package confluentmetrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Aggregations []struct{ Metric string } `json:"aggregations"`
			Filter       struct{ Value string }    `json:"filter"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if u, p, _ := r.BasicAuth(); u != "k" || p != "s" || req.Filter.Value != "lkc-1" {
			w.WriteHeader(401)
			return
		}
		switch {
		case strings.HasSuffix(req.Aggregations[0].Metric, "ccu_minutes"):
			_, _ = w.Write([]byte(`{"data":[{"timestamp":"2026-11-04T00:00:00Z","value":2,"metric.topic":"inventory_positions"},{"timestamp":"2026-11-04T00:01:00Z","value":3,"metric.topic":"inventory_positions"},{"timestamp":"2026-11-04T00:01:00Z","value":50,"metric.topic":"other"}]}`))
		case strings.HasSuffix(req.Aggregations[0].Metric, "dp_scanned_bytes"):
			_, _ = w.Write([]byte(`{"data":[{"timestamp":"2026-11-04T00:01:00Z","value":1000,"metric.topic":"inventory_positions"}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[{"timestamp":"2026-11-04T00:00:00Z","value":10,"metric.topic":"inventory_positions"},{"timestamp":"2026-11-04T00:01:00Z","value":12,"metric.topic":"inventory_positions"}]}`))
		}
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, Key: "k", Secret: "s", ClusterID: "lkc-1", Topic: "inventory_positions", HTTP: srv.Client()}
	u, err := c.Usage(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if u.CCUMinutes != 5 || u.ScannedBytes != 1000 || u.StorageBytes != 12 || u.AsOf == 0 {
		t.Fatalf("%+v", u)
	}
	c.Key = "bad"
	if _, err := c.Usage(context.Background(), time.Now()); err == nil {
		t.Fatal("401 accepted")
	}
}
