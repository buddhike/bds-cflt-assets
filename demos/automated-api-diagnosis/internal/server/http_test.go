package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"apidiag/internal/health"
	"apidiag/internal/simulator"
)

var testAssets = fstest.MapFS{
	"index.html":     {Data: []byte("<html>app</html>")},
	"assets/app.js":  {Data: []byte("console.log(1)")},
	"assets/app.css": {Data: []byte("a{}")},
}

func newServer(t *testing.T) (*Service, *httptest.Server, *feed) {
	t.Helper()
	svc, _, f := newService(t, testConfig())
	svc.Start(context.Background())
	ts := httptest.NewServer(svc.Handler(testAssets))
	t.Cleanup(ts.Close)
	return svc, ts, f
}

// client signs in as Launchpad's link does, and keeps the cookie.
func signedIn(t *testing.T, ts *httptest.Server, token string) *http.Client {
	t.Helper()
	jar := newJar()
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(ts.URL + "/?access=" + token)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return c
}

func do(t *testing.T, c *http.Client, method, url, body string, header ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestHealthzNeedsNoToken(t *testing.T) {
	_, ts, _ := newServer(t)
	res, err := http.Get(ts.URL + "/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", res, err)
	}
}

func TestNothingElseIsServedWithoutTheToken(t *testing.T) {
	_, ts, _ := newServer(t)
	for _, p := range []string{"/", "/index.html", "/assets/app.js", "/api/state", "/api/apis", "/api/events", "/api/health/backlog", "/api/reports/backlog"} {
		res, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s answered %d without a token, want 401", p, res.StatusCode)
		}
	}
	res, _ := http.Get(ts.URL + "/?access=wrong-wrong-wrong-wrong-wrong-wrong")
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a wrong token answered %d", res.StatusCode)
	}
}

func TestTokenInTheLinkBecomesACookieAndLeavesTheAddress(t *testing.T) {
	svc, ts, _ := newServer(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(ts.URL + "/?access=" + svc.cfg.AccessToken + "&keep=1")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/?keep=1" {
		t.Errorf("redirected to %q, which should drop the token and keep the rest", loc)
	}
	var cookie *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == CookieName {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie: %+v", cookie)
	}
	if strings.Contains(cookie.Value, svc.cfg.AccessToken) {
		t.Error("the cookie holds the token itself")
	}
	if cookie.Secure {
		t.Error("secure over plain HTTP: it would never come back")
	}

	req, _ := http.NewRequest("GET", ts.URL+"/?access="+svc.cfg.AccessToken, nil)
	req.Header.Set("CloudFront-Forwarded-Proto", "https")
	res, _ = c.Do(req)
	res.Body.Close()
	for _, ck := range res.Cookies() {
		if ck.Name == CookieName && !ck.Secure {
			t.Error("the cookie is not Secure although the viewer used HTTPS")
		}
	}
}

func TestSignedInViewerGetsTheUIAndTheState(t *testing.T) {
	svc, ts, _ := newServer(t)
	c := signedIn(t, ts, svc.cfg.AccessToken)

	res := do(t, c, "GET", ts.URL+"/some/client/route", "")
	if res.StatusCode != 200 {
		t.Fatalf("page: %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") || res.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("security headers missing: %q", got)
	}

	res = do(t, c, "GET", ts.URL+"/api/state", "")
	var st State
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.ClusterName != svc.cfg.DemoName || st.TopicName != DefaultTopic || st.Region != "us-east-1" {
		t.Errorf("state: %+v", st)
	}

	res = do(t, c, "GET", ts.URL+"/api/apis", "")
	var apis APIs
	if err := json.NewDecoder(res.Body).Decode(&apis); err != nil || len(apis.Current) != len(simulator.DefaultAPIs()) {
		t.Fatalf("apis: %v %+v", err, apis)
	}

	res = do(t, c, "GET", ts.URL+"/api/nope", "")
	if res.StatusCode != http.StatusNotFound || !strings.Contains(res.Header.Get("Content-Type"), "json") {
		t.Errorf("unknown endpoint: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

func TestChangingThingsNeedsTheCSRFHeader(t *testing.T) {
	svc, ts, _ := newServer(t)
	c := signedIn(t, ts, svc.cfg.AccessToken)
	for _, p := range []string{"/api/simulation/start", "/api/simulation/stop", "/api/apis/update"} {
		res := do(t, c, "POST", ts.URL+p, "{}")
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s without the header answered %d, want 403", p, res.StatusCode)
		}
	}
	if svc.State().Running {
		t.Error("a request without the header started the simulation")
	}
}

func TestStartStopAndUpdateOverHTTP(t *testing.T) {
	svc, ts, _ := newServer(t)
	c := signedIn(t, ts, svc.cfg.AccessToken)
	h := []string{CSRFHeader, "1", "Content-Type", "application/json"}

	if res := do(t, c, "POST", ts.URL+"/api/simulation/start", "", h...); res.StatusCode != 200 || !svc.State().Running {
		t.Fatalf("start with no body: %d running=%v", res.StatusCode, svc.State().Running)
	}
	name := simulator.DefaultAPIs()[2].Name
	res := do(t, c, "POST", ts.URL+"/api/apis/update", `{"name":"`+name+`","rate":3,"latencyMs":50,"gatewayError":true}`, h...)
	var out struct{ Running bool }
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 200 || !out.Running || !svc.APIs().Current[2].GatewayError {
		t.Fatalf("update: %d %+v", res.StatusCode, out)
	}
	if res := do(t, c, "POST", ts.URL+"/api/apis/update", `{"name":"`+name+`","rate":1000,"latencyMs":50}`, h...); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a flooding rate answered %d, want 400", res.StatusCode)
	}
	if res := do(t, c, "POST", ts.URL+"/api/apis/update", `not json`, h...); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad JSON answered %d, want 400", res.StatusCode)
	}
	if res := do(t, c, "POST", ts.URL+"/api/simulation/stop", "", h...); res.StatusCode != 200 || svc.State().Running {
		t.Fatalf("stop: %d running=%v", res.StatusCode, svc.State().Running)
	}
}

func TestEventStreamCarriesEvents(t *testing.T) {
	svc, ts, f := newServer(t)
	c := signedIn(t, ts, svc.cfg.AccessToken)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	f.health <- health.HealthEvent{APIID: "BOOKING_API_V1", HTTPStatus: "500", IsAnomaly: true}

	sc := bufio.NewScanner(res.Body)
	var name string
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			name = v
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok && name == "health:event" {
			var ev health.HealthEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil || ev.APIID != "BOOKING_API_V1" || !ev.IsAnomaly {
				t.Fatalf("event: %v %+v", err, ev)
			}
			return
		}
	}
	t.Fatalf("the stream ended without the event: %v", sc.Err())
}
