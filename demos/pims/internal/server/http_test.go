package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"pims/internal/compose"
	"pims/internal/config"
	"pims/internal/latency"
	"pims/internal/pipeline"
	"pims/internal/query"
	"pims/internal/sim"
	"pims/internal/terraform"
)

type nopStore struct{}

func (nopStore) Load(context.Context, time.Time) (sim.Snapshot, error)                { return sim.Snapshot{}, nil }
func (nopStore) InsertPatient(context.Context, sim.Patient) (int64, error)            { return 1, nil }
func (nopStore) UpdatePatientContact(context.Context, int64, sim.ContactChange) error { return nil }
func (nopStore) InsertAppointment(context.Context, sim.Appointment) (int64, error)    { return 1, nil }
func (nopStore) SetAppointmentStatus(context.Context, int64, string) error            { return nil }
func (nopStore) CheckIn(context.Context, sim.Visit) (int64, error)                    { return 1, nil }
func (nopStore) CompleteVisit(context.Context, sim.Visit, time.Time, string) error    { return nil }

type closer struct{}

func (closer) Close() error { return nil }

type okQuerier struct{}

func (okQuerier) Query(context.Context) (latency.Result, error) { return latency.Result{}, nil }

// fakeDeps succeed at once. block, if set, holds the services step until it is closed.
func fakeDeps(block chan struct{}) pipeline.Deps {
	return pipeline.Deps{
		Titles:      Titles,
		CheckDocker: func(context.Context) error { return nil },
		Provision: func(_ context.Context, _ config.Credentials, o terraform.ApplyOptions) (terraform.Outputs, error) {
			return terraform.Outputs{KafkaClusterID: "lkc-1", Topics: []string{"patients"}, TableflowEnabled: o.EnableTableflow}, nil
		},
		Destroy: func(context.Context, config.Credentials) error { return ErrManaged },
		StartLocal: func(ctx context.Context, _ terraform.Outputs) (compose.MySQLSecrets, error) {
			if block != nil {
				select {
				case <-block:
				case <-ctx.Done():
					return compose.MySQLSecrets{}, ctx.Err()
				}
			}
			return compose.MySQLSecrets{Generation: "abc123abc123"}, nil
		},
		StopLocal: func(context.Context, bool) error { return ErrManaged },
		PrepareDB: func(context.Context, compose.MySQLSecrets) (sim.Store, io.Closer, error) {
			return nopStore{}, closer{}, nil
		},
		StartCDC:    func(context.Context, terraform.Outputs, compose.MySQLSecrets) error { return nil },
		StopCDC:     func(context.Context, compose.MySQLSecrets) error { return ErrManaged },
		WaitSchemas: func(context.Context, terraform.Outputs) error { return nil },
		NewQuerier:  func(config.Credentials, terraform.Outputs) (latency.Querier, error) { return okQuerier{}, nil },
		RunQuery: func(_ context.Context, o terraform.Outputs, sql string) (query.Result, error) {
			if strings.TrimSpace(sql) == "" {
				return query.Result{}, query.ErrEmpty // as query.Runner does
			}
			return query.Result{Columns: []string{"tableflow"}, TotalRows: 1}, nil
		},
		PollInterval: time.Hour,
	}
}

var assets = fstest.MapFS{
	"index.html":     {Data: []byte("<html>PIMS</html>")},
	"assets/app.js":  {Data: []byte("console.log(1)")},
	"assets/app.css": {Data: []byte("body{}")},
}

type rig struct {
	svc *Service
	srv *httptest.Server
	tok string
}

func newRig(t *testing.T, block chan struct{}) *rig {
	t.Helper()
	cfg := testConfig(t)
	svc, err := New(cfg, slog.NewTextHandler(io.Discard, nil), func(*slog.Logger) (pipeline.Deps, error) { return fakeDeps(block), nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(svc.Handler(assets))
	t.Cleanup(func() { srv.Close(); svc.Close(5 * time.Second) })
	return &rig{svc: svc, srv: srv, tok: cfg.AccessToken}
}

// client keeps cookies but does not follow redirects, so a test can see them.
func (r *rig) client() *http.Client {
	jar := &simpleJar{}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type simpleJar struct{ cookies []*http.Cookie }

func (j *simpleJar) SetCookies(_ *url.URL, c []*http.Cookie) { j.cookies = append(j.cookies, c...) }
func (j *simpleJar) Cookies(*url.URL) []*http.Cookie         { return j.cookies }

func (r *rig) signedIn(t *testing.T) *http.Client {
	t.Helper()
	c := r.client()
	res, err := c.Get(r.srv.URL + "/?" + AccessParam + "=" + r.tok)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in status %d", res.StatusCode)
	}
	return c
}

func do(t *testing.T, c *http.Client, method, url string, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

var post = map[string]string{CSRFHeader: "1", "Content-Type": "application/json"}

func TestNothingIsServedWithoutTheToken(t *testing.T) {
	r := newRig(t, nil)
	c := r.client()
	for _, p := range []string{"/", "/api/state", "/index.html", "/assets/app.js"} {
		res, body := do(t, c, "GET", r.srv.URL+p, "", nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d", p, res.StatusCode)
		}
		if strings.Contains(body, "PIMS</html>") {
			t.Errorf("%s: leaked the page", p)
		}
	}
	res, _ := do(t, c, "GET", r.srv.URL+"/?"+AccessParam+"=wrong-token-wrong-token-xx", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: status %d", res.StatusCode)
	}
	res, body := do(t, c, "GET", r.srv.URL+"/api/state", "", nil)
	if res.Header.Get("Content-Type") != "application/json" || !strings.Contains(body, `"error"`) {
		t.Errorf("api 401 is not JSON: %q", body)
	}
}

func TestHealthCheckNeedsNoToken(t *testing.T) {
	r := newRig(t, nil)
	res, body := do(t, r.client(), "GET", r.srv.URL+"/healthz", "", nil)
	if res.StatusCode != http.StatusOK || body != "ok" {
		t.Fatalf("%d %q", res.StatusCode, body)
	}
}

func TestTheLinkSetsAHardenedCookieAndHidesTheToken(t *testing.T) {
	r := newRig(t, nil)
	c := r.client()
	res, _ := do(t, c, "GET", r.srv.URL+"/?"+AccessParam+"="+r.tok+"&keep=1", "", nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/?keep=1" {
		t.Fatalf("status %d, location %q", res.StatusCode, res.Header.Get("Location"))
	}
	var got *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == CookieName {
			got = ck
		}
	}
	if got == nil || !got.HttpOnly || got.SameSite != http.SameSiteStrictMode || got.Secure {
		t.Fatalf("cookie = %+v", got)
	}
	if strings.Contains(got.Value, r.tok) {
		t.Error("the cookie holds the token itself")
	}
	res, _ = do(t, c, "GET", r.srv.URL+"/?"+AccessParam+"="+r.tok, "", map[string]string{"X-Forwarded-Proto": "https"})
	for _, ck := range res.Cookies() {
		if ck.Name == CookieName && !ck.Secure {
			t.Error("the cookie is not Secure behind HTTPS")
		}
	}
	res, body := do(t, c, "GET", r.srv.URL+"/", "", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "PIMS") {
		t.Fatalf("with the cookie: %d %q", res.StatusCode, body)
	}
}

func TestAPIPostsNeedTheCSRFHeader(t *testing.T) {
	r := newRig(t, nil)
	c := r.signedIn(t)
	res, _ := do(t, c, "POST", r.srv.URL+"/api/simulation/stop", "", nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("without the header: %d", res.StatusCode)
	}
	res, body := do(t, c, "POST", r.srv.URL+"/api/query", `{"sql":"select 1"}`, post)
	if res.StatusCode != http.StatusConflict {
		t.Errorf("before setup: %d %s", res.StatusCode, body)
	}
}

func TestSecurityHeadersOnEverything(t *testing.T) {
	r := newRig(t, nil)
	c := r.signedIn(t)
	for _, p := range []string{"/", "/api/state"} {
		res, _ := do(t, c, "GET", r.srv.URL+p, "", nil)
		for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
			if res.Header.Get(h) == "" {
				t.Errorf("%s lacks %s", p, h)
			}
		}
	}
}

func TestStaticFilesAndTheAppPageFallback(t *testing.T) {
	r := newRig(t, nil)
	c := r.signedIn(t)
	res, body := do(t, c, "GET", r.srv.URL+"/assets/app.js", "", nil)
	if res.StatusCode != http.StatusOK || body != "console.log(1)" || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %q %q", res.StatusCode, body, res.Header.Get("Cache-Control"))
	}
	res, body = do(t, c, "GET", r.srv.URL+"/some/page", "", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "PIMS") || strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("fallback: %d %q %q", res.StatusCode, body, res.Header.Get("Cache-Control"))
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSetupRunsOnStartAndTheUISeesIt(t *testing.T) {
	block := make(chan struct{})
	r := newRig(t, block)
	r.svc.Start(context.Background())
	c := r.signedIn(t)

	var st State
	get := func() State {
		_, body := do(t, c, "GET", r.srv.URL+"/api/state", "", nil)
		var s State
		if err := json.Unmarshal([]byte(body), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	waitFor(t, "setup to be running", func() bool { st = get(); return st.Busy == "setup" })
	if !st.Managed || !st.HasCredentials || st.AWSRegion != "us-east-1" {
		t.Errorf("state: %+v", st)
	}
	if st.Steps[0].Title != "Check configuration" {
		t.Errorf("titles not overridden: %q", st.Steps[0].Title)
	}
	// Setup is already running: asking again is a conflict, not a second run.
	if res, _ := do(t, c, "POST", r.srv.URL+"/api/setup", "", post); res.StatusCode != http.StatusConflict {
		t.Errorf("second setup: %d", res.StatusCode)
	}
	close(block)
	waitFor(t, "setup to finish", func() bool { st = get(); return st.Busy == "" && st.Sim.Running })
	for _, s := range st.Steps {
		if s.Status != "done" {
			t.Errorf("step %s is %s", s.ID, s.Status)
		}
	}
	if len(st.Logs) == 0 {
		t.Error("the UI got no log history")
	}
	if st.Query.Catalog != query.CatalogName || st.Query.Namespace != "lkc-1" {
		t.Errorf("query info: %+v", st.Query)
	}
}

func TestEventsAreStreamed(t *testing.T) {
	r := newRig(t, nil)
	c := r.signedIn(t)
	req, _ := http.NewRequest("GET", r.srv.URL+"/api/events", nil)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", res.Header.Get("Content-Type"))
	}
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	r.svc.hub.publish("step", map[string]string{"id": "schema"})
	deadline := time.After(5 * time.Second)
	var sawEvent, sawData bool
	for !(sawEvent && sawData) {
		select {
		case l := <-lines:
			sawEvent = sawEvent || l == "event: step"
			sawData = sawData || l == `data: {"id":"schema"}`
		case <-deadline:
			t.Fatalf("event %v data %v", sawEvent, sawData)
		}
	}
}

func TestErrorsMapToStatuses(t *testing.T) {
	r := newRig(t, nil)
	r.svc.Start(context.Background())
	c := r.signedIn(t)
	waitFor(t, "setup", func() bool {
		_, b := do(t, c, "GET", r.srv.URL+"/api/state", "", nil)
		return strings.Contains(b, `"busy":""`)
	})
	if res, _ := do(t, c, "POST", r.srv.URL+"/api/simulation/start", `{"rate":2}`, post); res.StatusCode != http.StatusConflict {
		t.Errorf("start while running: %d", res.StatusCode) // setup already started it
	}
	if res, _ := do(t, c, "POST", r.srv.URL+"/api/simulation/stop", "", post); res.StatusCode != http.StatusOK {
		t.Errorf("stop: %d", res.StatusCode)
	}
	if res, _ := do(t, c, "POST", r.srv.URL+"/api/simulation/start", `{"rate":2}`, post); res.StatusCode != http.StatusOK {
		t.Errorf("start: %d", res.StatusCode)
	}
	if res, _ := do(t, c, "POST", r.srv.URL+"/api/simulation/rate", "not json", post); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad body: %d", res.StatusCode)
	}
	if res, _ := do(t, c, "POST", r.srv.URL+"/api/query", `{"sql":"  "}`, post); res.StatusCode != http.StatusBadRequest {
		t.Errorf("empty sql: %d", res.StatusCode)
	}
	res, body := do(t, c, "POST", r.srv.URL+"/api/query", `{"sql":"select 1"}`, post)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "tableflow") {
		t.Errorf("query: %d %s", res.StatusCode, body)
	}
}
