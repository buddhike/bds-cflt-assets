package runner_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"launchpad/internal/runner"
	"launchpad/internal/runs"
)

const hookToken = "tok-0123456789abcdef-0123456789"

// demoServer stands in for a demo's HTTPS endpoint. It records each request and what
// Terraform had been asked to do when it arrived.
type demoServer struct {
	*httptest.Server
	mu       sync.Mutex
	reqs     []*http.Request
	before   []bool // whether terraform destroy had been called when each request came in
	statuses []int  // the status of each request in turn; the last one repeats
	body     string
}

func (f *fixture) demoServer(t *testing.T, body string, statuses ...int) *demoServer {
	t.Helper()
	d := &demoServer{statuses: statuses, body: body}
	d.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.reqs = append(d.reqs, r)
		d.before = append(d.before, f.called("terraform destroy") == 0)
		status := d.statuses[min(len(d.reqs)-1, len(d.statuses)-1)]
		w.WriteHeader(status)
		_, _ = w.Write([]byte(d.body))
	}))
	t.Cleanup(d.Close)
	f.runner.HTTP = d.Client()
	f.runner.ShutdownBackoff = time.Millisecond
	return d
}

func (d *demoServer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.reqs)
}

// tearDown launches the plain demo, gives its Terraform the hook output, and runs the teardown.
func (f *fixture) tearDown(t *testing.T, hookURL string) string {
	t.Helper()
	f.run(t, f.launch(t, "plain"), 900*time.Second)
	out, _ := json.Marshal(map[string]string{"url": hookURL, "token": hookToken})
	f.hook = string(out)
	f.install("ok", "http://demo.example")
	if _, err := runs.RequestTeardown(ctx, f.Services, "plain", ann); err != nil {
		t.Fatal(err)
	}
	return f.run(t, runner.Event{DemoID: "plain", RunID: f.state("plain").RunID}, 900*time.Second)
}

func TestTeardownAsksTheDemoToShutDownBeforeDestroying(t *testing.T) {
	f := newFixture(t)
	d := f.demoServer(t, `{"ok":true}`, 200)
	if got := f.tearDown(t, d.URL+"/shutdown"); got != "destroyed" {
		t.Fatalf("result %q", got)
	}
	if d.count() != 1 {
		t.Fatalf("%d requests, want 1", d.count())
	}
	r := d.reqs[0]
	if r.Method != "POST" || r.URL.Path != "/shutdown" || r.Header.Get("Authorization") != "Bearer "+hookToken {
		t.Errorf("request: %s %s, Authorization %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
	}
	if !d.before[0] {
		t.Error("the demo was asked after Terraform had begun to destroy it")
	}
	if f.called("terraform destroy -auto-approve") != 1 {
		t.Errorf("calls %v", f.calls())
	}
}

func TestADemoThatSaysItFailedStopsTheTeardownAndTheTokenStaysOutOfTheMessage(t *testing.T) {
	f := newFixture(t)
	d := f.demoServer(t, `{"error":"disable Tableflow on patients: the API said no (`+hookToken+`)"}`, 500)
	if got := f.tearDown(t, d.URL+"/shutdown"); got != "failed" {
		t.Fatalf("result %q", got)
	}
	st := f.state("plain")
	if st.Status != runs.TeardownFailed || !strings.Contains(st.Error, "could not shut down") || !strings.Contains(st.Error, "the API said no") {
		t.Fatalf("state %+v", st)
	}
	if strings.Contains(st.Error, hookToken) || strings.Contains(st.LogTail, hookToken) {
		t.Errorf("the token leaked: %q / %q", st.Error, st.LogTail)
	}
	if f.called("terraform destroy") != 0 {
		t.Error("Terraform destroyed a demo that had not shut down")
	}
	if d.count() != 1 {
		t.Errorf("%d requests: a refusal from the demo is not tried again", d.count())
	}
}

func TestADemoThatDoesNotAnswerIsDestroyedAnyway(t *testing.T) {
	f := newFixture(t)
	d := f.demoServer(t, "", 503)
	if got := f.tearDown(t, d.URL+"/shutdown"); got != "destroyed" {
		t.Fatalf("result %q", got)
	}
	if d.count() != 3 {
		t.Errorf("%d requests, want 3", d.count())
	}
	if f.called("terraform destroy -auto-approve") != 1 {
		t.Errorf("calls %v", f.calls())
	}
}

func TestADemoThatCannotBeReachedIsDestroyedAnyway(t *testing.T) {
	f := newFixture(t)
	d := f.demoServer(t, "", 200)
	url := d.URL + "/shutdown"
	d.Close() // nobody is listening any more
	if got := f.tearDown(t, url); got != "destroyed" {
		t.Fatalf("result %q", got)
	}
}

func TestADemoAnswersAfterATryThatGotNoAnswer(t *testing.T) {
	f := newFixture(t)
	d := f.demoServer(t, `{"ok":true}`, 504, 200)
	if got := f.tearDown(t, d.URL+"/shutdown"); got != "destroyed" || d.count() != 2 {
		t.Fatalf("result %q after %d requests", got, d.count())
	}
}

func TestTheShutdownAddressMustBeHTTPS(t *testing.T) {
	f := newFixture(t)
	d := f.demoServer(t, "", 200)
	plain := strings.Replace(d.URL, "https://", "http://", 1) + "/shutdown"
	if got := f.tearDown(t, plain); got != "destroyed" {
		t.Fatalf("result %q", got)
	}
	if d.count() != 0 {
		t.Error("the token was sent over plain HTTP")
	}
	if !strings.Contains(f.Logs(), "Unusable shutdown output") {
		t.Errorf("nothing says why the demo was not asked: %q", f.Logs())
	}
}

func TestADemoWithoutAShutdownOutputIsNotAsked(t *testing.T) {
	f := newFixture(t)
	d := f.demoServer(t, "", 200)
	f.run(t, f.launch(t, "plain"), 900*time.Second)
	_, _ = runs.RequestTeardown(ctx, f.Services, "plain", ann)
	if got := f.run(t, runner.Event{DemoID: "plain", RunID: f.state("plain").RunID}, 900*time.Second); got != "destroyed" || d.count() != 0 {
		t.Fatalf("result %q, %d requests", got, d.count())
	}
}

func TestAResumedTeardownDoesNotAskAgain(t *testing.T) {
	f := newFixture(t)
	d := f.demoServer(t, `{"ok":true}`, 200)
	f.run(t, f.launch(t, "plain"), 900*time.Second)
	out, _ := json.Marshal(map[string]string{"url": d.URL + "/shutdown", "token": hookToken})
	f.hook = string(out)
	f.install("ok", "http://demo.example")
	_, _ = runs.RequestTeardown(ctx, f.Services, "plain", ann)
	ev := runner.Event{DemoID: "plain", RunID: f.state("plain").RunID, Resumes: 1}
	if got := f.run(t, ev, 900*time.Second); got != "destroyed" || d.count() != 0 {
		t.Fatalf("result %q, %d requests", got, d.count())
	}
}
