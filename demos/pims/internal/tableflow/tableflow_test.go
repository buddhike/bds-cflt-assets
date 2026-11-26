package tableflow

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type call struct {
	method, path, query, user, pass string
	body                            string
}

// server answers each request with the next status of its method, then the last one again.
func server(t *testing.T, post, del []int) (*Client, *[]call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	next := map[string]*[]int{http.MethodPost: &post, http.MethodDelete: &del}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		u, p, _ := r.BasicAuth()
		calls = append(calls, call{r.Method, r.URL.Path, r.URL.RawQuery, u, p, string(b)})
		q := next[r.Method]
		status := (*q)[0]
		if len(*q) > 1 {
			*q = (*q)[1:]
		}
		w.WriteHeader(status)
		if status >= 400 {
			_, _ = io.WriteString(w, `{"errors":[{"detail":"the reason"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "KEY", "secret", "env-1", "lkc-1", quiet)
	if err != nil {
		t.Fatal(err)
	}
	c.policy.Initial, c.policy.Max = time.Millisecond, time.Millisecond
	return c, &calls
}

func TestEnableAsksForIcebergTablesInManagedStorageForEachTopic(t *testing.T) {
	c, calls := server(t, []int{202}, nil)
	if err := c.Enable(context.Background(), []string{"patients", "visits"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("%d calls, want 2", len(*calls))
	}
	got := (*calls)[0]
	if got.method != "POST" || got.path != "/tableflow/v1/tableflow-topics" || got.user != "KEY" || got.pass != "secret" {
		t.Errorf("request: %+v", got)
	}
	var body struct {
		Spec struct {
			DisplayName  string            `json:"display_name"`
			Suspended    bool              `json:"suspended"`
			TableFormats []string          `json:"table_formats"`
			Storage      map[string]string `json:"storage"`
			Environment  map[string]string `json:"environment"`
			KafkaCluster map[string]string `json:"kafka_cluster"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(got.body), &body); err != nil {
		t.Fatal(err)
	}
	s := body.Spec
	if s.DisplayName != "patients" || s.Suspended || len(s.TableFormats) != 1 || s.TableFormats[0] != "ICEBERG" ||
		s.Storage["kind"] != "Managed" || s.Environment["id"] != "env-1" || s.KafkaCluster["id"] != "lkc-1" {
		t.Errorf("body: %s", got.body)
	}
}

func TestEnableLeavesATopicThatAlreadyHasTableflow(t *testing.T) {
	c, _ := server(t, []int{409}, nil)
	if err := c.Enable(context.Background(), []string{"patients"}); err != nil {
		t.Fatal(err)
	}
}

func TestEnableTriesAgainWhileTheSchemaIsNotKnownYet(t *testing.T) {
	c, calls := server(t, []int{422, 429, 503, 202}, nil)
	if err := c.Enable(context.Background(), []string{"patients"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 4 {
		t.Errorf("%d calls, want 4", len(*calls))
	}
}

func TestEnableDoesNotRetryARefusedKeyAndSaysWhy(t *testing.T) {
	c, calls := server(t, []int{403}, nil)
	err := c.Enable(context.Background(), []string{"patients"})
	if err == nil || len(*calls) != 1 {
		t.Fatalf("err = %v after %d calls", err, len(*calls))
	}
	for _, want := range []string{"patients", "403", "the reason"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaks the secret: %v", err)
	}
}

func TestEnableGivesUpWhenTheContextEnds(t *testing.T) {
	c, _ := server(t, []int{422}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := c.Enable(ctx, []string{"patients"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDisableDeletesWithTheScopeInTheQuery(t *testing.T) {
	c, calls := server(t, nil, []int{204})
	if err := c.Disable(context.Background(), []string{"patients", "visits"}); err != nil {
		t.Fatal(err)
	}
	got := (*calls)[1]
	if got.method != "DELETE" || got.path != "/tableflow/v1/tableflow-topics/visits" ||
		got.query != "environment=env-1&spec.kafka_cluster=lkc-1" {
		t.Errorf("request: %+v", got)
	}
}

func TestDisableSkipsATopicThatHasNoTableflow(t *testing.T) {
	c, _ := server(t, nil, []int{404})
	if err := c.Disable(context.Background(), []string{"patients"}); err != nil {
		t.Fatal(err)
	}
}

func TestDisableTriesEveryTopicAndReportsAllFailures(t *testing.T) {
	c, calls := server(t, nil, []int{403})
	err := c.Disable(context.Background(), []string{"patients", "visits"})
	if err == nil || len(*calls) != 2 {
		t.Fatalf("err = %v after %d calls", err, len(*calls))
	}
	if !strings.Contains(err.Error(), "patients") || !strings.Contains(err.Error(), "visits") {
		t.Errorf("error does not name both topics: %v", err)
	}
}

func TestNewRefusesAKeyOverPlainHTTPToAnotherHost(t *testing.T) {
	if _, err := New("http://api.example.com", "k", "s", "env-1", "lkc-1", quiet); err == nil {
		t.Error("accepted http to a remote host")
	}
	if _, err := New(DefaultEndpoint, "", "s", "env-1", "lkc-1", quiet); err == nil {
		t.Error("accepted an empty key")
	}
}

func TestDisableGivesEachTopicItsOwnTimeToTry(t *testing.T) {
	c, calls := server(t, nil, []int{503})
	c.perTopic = 20 * time.Millisecond
	c.policy.Initial, c.policy.Max = 5*time.Millisecond, 5*time.Millisecond
	c.policy.Attempts = 1000
	err := c.Disable(context.Background(), []string{"patients", "visits"})
	if err == nil {
		t.Fatal("expected an error")
	}
	var seen = map[string]bool{}
	for _, cl := range *calls {
		seen[cl.path] = true
	}
	if !seen["/tableflow/v1/tableflow-topics/patients"] || !seen["/tableflow/v1/tableflow-topics/visits"] {
		t.Errorf("the second topic was never tried: %v", seen)
	}
}
