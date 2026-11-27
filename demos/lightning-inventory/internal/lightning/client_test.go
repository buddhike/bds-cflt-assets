package lightning

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueries(t *testing.T) {
	if got := SKUQuery("inventory_positions", "SKU-0000001"); got != "SELECT sku, location_id, quantity FROM `inventory_positions` WHERE sku = 'SKU-0000001'" {
		t.Fatal(got)
	}
	got := LocationsQuery("t", "SKU-1", []string{"LOC-1", "LOC-2"})
	if got != "SELECT sku, location_id, quantity FROM `t` WHERE sku = 'SKU-1' AND location_id IN ('LOC-1', 'LOC-2')" {
		t.Fatal(got)
	}
	if !ValidTopic("inventory_positions") || ValidTopic("a-b") || ValidTopic("`x") {
		t.Fatal("topic validation")
	}
}

func TestQuery(t *testing.T) {
	var status = http.StatusOK
	var gotBody map[string]any
	var user, pass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ = r.BasicAuth()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"result":{"data":[["a"],["b"]]}}`))
	}))
	defer srv.Close()
	q := &Querier{Endpoint: srv.URL, Catalog: "env-1", Database: "lkc-1", Auth: Auth{"k", "s"}, HTTP: srv.Client()}
	rows, err := q.Query(context.Background(), "SELECT 1")
	if err != nil || rows != 2 {
		t.Fatal(rows, err)
	}
	if user != "k" || pass != "s" || gotBody["catalog_name"] != "env-1" || gotBody["database_name"] != "lkc-1" || gotBody["query"] != "SELECT 1" {
		t.Fatalf("request: %v %v %v", user, pass, gotBody)
	}
	for st, want := range map[int]error{202: ErrAsync, 429: ErrRateLimited, 404: ErrNotFound} {
		status = st
		if _, err := q.Query(context.Background(), "x"); err != want {
			t.Errorf("%d: %v", st, err)
		}
	}
	status = 500
	if _, err := q.Query(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("500: %v", err)
	}
}

func TestTableStatus(t *testing.T) {
	var calls []string
	var ua, user string
	status := http.StatusOK
	body := `{"status":{"phase":"ACTIVE"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		ua = r.Header.Get("User-Agent")
		user, _, _ = r.BasicAuth()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	tb := NewTables("env-1", "lkc-1", "inventory_positions", Auth{"k", "s"}, srv.Client())
	tb.BaseURL = srv.URL
	ctx := context.Background()
	if p, _, err := tb.Status(ctx); err != nil || p != PhaseActive {
		t.Fatal(p, err)
	}
	if want := "GET /rtce/v1/rtce-topics/inventory_positions?environment=env-1&spec.kafka_cluster=lkc-1"; calls[0] != want {
		t.Fatalf("got %q", calls[0])
	}
	if ua == "" || user != "k" {
		t.Fatalf("user agent %q, user %q", ua, user)
	}
	body = `{"status":{"phase":"FAILED","error_message":"nope"}}`
	if p, msg, err := tb.Status(ctx); err != nil || p != PhaseFailed || msg != "nope" {
		t.Fatal(p, msg, err)
	}
	status, body = 404, ""
	if _, _, err := tb.Status(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	status = 429
	if _, _, err := tb.Status(ctx); !errors.Is(err, ErrRateLimited) {
		t.Fatal(err)
	}
	status = 403
	_, _, err := tb.Status(ctx)
	var se *StatusError
	if !errors.As(err, &se) || !se.Client() {
		t.Fatalf("403: %v", err)
	}
	status = 503
	if _, _, err = tb.Status(ctx); !errors.As(err, &se) || se.Client() {
		t.Fatalf("503: %v", err)
	}
	// the client never writes: only GET reaches the API
	for _, c := range calls {
		if !strings.HasPrefix(c, "GET ") {
			t.Fatalf("a call that is not a read: %s", c)
		}
	}
}
