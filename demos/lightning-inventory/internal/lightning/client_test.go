package lightning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	locs := make([]string, 45)
	for i := range locs {
		locs[i] = fmt.Sprintf("L%d", i)
	}
	got = LocationsQuery("t", "S", locs)
	if n := strings.Count(got, "location_id IN ("); n != 3 || !strings.Contains(got, "AND (location_id IN ('L0', ") || !strings.HasSuffix(got, "'L44'))") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "'L19') OR location_id IN ('L20'") || !strings.Contains(got, "'L39') OR location_id IN ('L40'") {
		t.Fatal("lists of 20", got)
	}
	if strings.Contains(LocationsQuery("t", "S", locs[:20]), "OR") {
		t.Fatal("twenty locations are one list")
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
		if status == 400 {
			_, _ = w.Write([]byte(`{"errors":[{"code":"bad_request","detail":"statement too long"}]}`))
			return
		}
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
	status = 400
	if _, err := q.Query(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "bad_request: statement too long") {
		t.Errorf("a 400 names what Confluent said: %v", err)
	}
}

func TestTableStatus(t *testing.T) {
	var calls []string
	var ua, user string
	status := http.StatusOK
	body := list("inventory_positions", "ACTIVE", "")
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
	if want := "GET /rtce/v1/rtce-topics?environment=env-1&spec.kafka_cluster=lkc-1"; calls[0] != want {
		t.Fatalf("got %q", calls[0])
	}
	if ua == "" || user != "k" {
		t.Fatalf("user agent %q, user %q", ua, user)
	}
	body = list("inventory_positions", "FAILED", "nope")
	if p, msg, err := tb.Status(ctx); err != nil || p != PhaseFailed || msg != "nope" {
		t.Fatal(p, msg, err)
	}
	status, body = 404, ""
	if _, _, err := tb.Status(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	status = 200
	for name, b := range map[string]string{
		"another table only": list("other_topic", "ACTIVE", ""),
		"an empty list":      `{"data":[]}`,
	} {
		body = b
		if _, _, err := tb.Status(ctx); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	body = `{"data":[` + item("other_topic", "ACTIVE", "") + `,` + item("inventory_positions", "PROVISIONING", "") + `]}`
	if p, _, err := tb.Status(ctx); err != nil || p != PhaseProvisioning {
		t.Fatal("the table among others:", p, err)
	}
	body = "not json"
	if _, _, err := tb.Status(ctx); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a body that is not the list is an error: %v", err)
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
	if se.Detail != "" {
		t.Fatalf("a 403 with no error body has no detail: %q", se.Detail)
	}
	status, body = 403, `{"errors":[{"code":"user_unauthorized","title":"User Access Unauthorized","detail":"The user 'sa-1' is not allowed to access the 'x' resource without the 'Y' role."}]}`
	_, _, err = tb.Status(ctx)
	if !errors.As(err, &se) || !strings.Contains(err.Error(), "user_unauthorized: The user 'sa-1' is not allowed") {
		t.Fatalf("a 403 names what Confluent said: %v", err)
	}
	status, body = 403, "<html>not json</html>"
	if _, _, err = tb.Status(ctx); err == nil || err.Error() != "unexpected HTTP status 403" {
		t.Fatalf("an answer that is not Confluent's error body adds nothing: %v", err)
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

func TestErrorDetail(t *testing.T) {
	long := strings.Repeat("é", 400)
	for name, c := range map[string]struct{ body, want string }{
		"empty":         {``, ""},
		"not json":      {`<html>`, ""},
		"no errors":     {`{"errors":[]}`, ""},
		"null error":    {`{"errors":[null]}`, ""},
		"wrong type":    {`{"errors":"x"}`, ""},
		"code, detail":  {`{"errors":[{"code":"c","title":"T","detail":"D"}]}`, "c: D"},
		"title instead": {`{"errors":[{"code":"c","title":"T"}]}`, "c: T"},
		"no code":       {`{"errors":[{"detail":"D"}]}`, "D"},
		"code only":     {`{"errors":[{"code":"c"}]}`, "c"},
		"one line":      {`{"errors":[{"code":"c","detail":"a\n\tb   c"}]}`, "c: a b c"},
		"rune safe":     {`{"errors":[{"detail":"` + long + `"}]}`, strings.Repeat("é", maxDetail) + "…"},
	} {
		if got := errorDetail([]byte(c.body)); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func item(topic, phase, msg string) string {
	return `{"spec":{"topic_name":"` + topic + `"},"status":{"phase":"` + phase + `","error_message":"` + msg + `"}}`
}

func list(topic, phase, msg string) string { return `{"data":[` + item(topic, phase, msg) + `]}` }

func TestTableStatusFollowsPages(t *testing.T) {
	var calls []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RequestURI())
		if r.URL.Query().Get("page_token") == "" {
			_, _ = w.Write([]byte(`{"data":[` + item("other_topic", "ACTIVE", "") + `],"metadata":{"next":"` + srv.URL + `/rtce/v1/rtce-topics?environment=env-1&spec.kafka_cluster=lkc-1&page_token=p2"}}`))
			return
		}
		_, _ = w.Write([]byte(list("inventory_positions", "ACTIVE", "")))
	}))
	defer srv.Close()
	tb := NewTables("env-1", "lkc-1", "inventory_positions", Auth{"k", "s"}, srv.Client())
	tb.BaseURL = srv.URL
	if p, _, err := tb.Status(context.Background()); err != nil || p != PhaseActive || len(calls) != 2 {
		t.Fatal(p, err, calls)
	}
}

// The key is sent with every request, so a next page on another host is not followed.
func TestTableStatusRefusesAnotherHost(t *testing.T) {
	var other int
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other++ }))
	defer evil.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"metadata":{"next":"` + evil.URL + `/x"}}`))
	}))
	defer srv.Close()
	tb := NewTables("env-1", "lkc-1", "inventory_positions", Auth{"k", "s"}, srv.Client())
	tb.BaseURL = srv.URL
	if _, _, err := tb.Status(context.Background()); err == nil || errors.Is(err, ErrNotFound) || other != 0 {
		t.Fatalf("err %v, calls to the other host %d", err, other)
	}
}

// A list that never ends is not followed for ever.
func TestTableStatusStopsAfterMaxPages(t *testing.T) {
	var n int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"data":[],"metadata":{"next":"` + srv.URL + `/again"}}`))
	}))
	defer srv.Close()
	tb := NewTables("env-1", "lkc-1", "inventory_positions", Auth{"k", "s"}, srv.Client())
	tb.BaseURL = srv.URL
	if _, _, err := tb.Status(context.Background()); err == nil || n != maxPages {
		t.Fatalf("err %v after %d pages", err, n)
	}
}
