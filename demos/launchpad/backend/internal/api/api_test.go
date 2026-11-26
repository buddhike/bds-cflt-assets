package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"launchpad/internal/api"
	"launchpad/internal/auth"
	"launchpad/internal/db"
	"launchpad/internal/runs"
	"launchpad/internal/testenv"
)

var ctx = context.Background()

type app struct {
	*testenv.Env
	t *testing.T
}

func newApp(t *testing.T) *app {
	env := testenv.New(t)
	env.GitHub.AddRepo(map[string]string{"demos/pims/tf/main.tf": testenv.CloudTF, "demos/plain/tf/main.tf": testenv.GoodTF})
	return &app{Env: env, t: t}
}

type reply struct {
	api.Response
	body map[string]any
}

func (a *app) call(method, path string, body any, token string, headers ...string) reply {
	a.t.Helper()
	h := map[string]string{"x-launchpad": "1"}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	cookies := map[string]string{}
	if token != "" {
		cookies[api.Cookie] = token
	}
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	return a.send(&api.Request{Method: method, Path: path, Headers: h, Cookies: cookies, Body: raw})
}

func (a *app) send(req *api.Request) reply {
	resp := api.Dispatch(ctx, a.Services, req)
	encoded, err := json.Marshal(resp.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	return reply{Response: resp, body: decoded}
}

func (a *app) signIn() reply {
	a.t.Helper()
	a.call("POST", "/auth/start", map[string]string{"phone": testenv.OwnerPhone}, "")
	return a.call("POST", "/auth/verify", map[string]string{"otp": a.SNS.LastCode()}, "")
}

func (a *app) token() string {
	a.t.Helper()
	r := a.signIn()
	if r.Status != 200 || len(r.Cookies) != 1 {
		a.t.Fatalf("sign-in: %d %v", r.Status, r.body)
	}
	return tokenOf(r.Cookies[0])
}

func tokenOf(cookie string) string {
	value, _, _ := strings.Cut(cookie, ";")
	_, token, _ := strings.Cut(value, "=")
	return token
}

func errorCode(r reply) string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestSignInSetsAHardenedCookie(t *testing.T) {
	a := newApp(t)
	r := a.signIn()
	user, _ := r.body["user"].(map[string]any)
	if r.Status != 200 || user["role"] != "admin" || user["email"] != testenv.OwnerEmail {
		t.Fatalf("%d %v", r.Status, r.body)
	}
	if _, leaked := r.body["token"]; leaked {
		t.Fatal("the token is in the body")
	}
	if _, leaked := user["pk"]; leaked {
		t.Fatal("storage keys are in the body")
	}
	cookie := r.Cookies[0]
	for _, flag := range []string{"HttpOnly", "Secure", "SameSite=Strict", "Path=/api", "Max-Age=43200", "lp_session="} {
		if !strings.Contains(cookie, flag) {
			t.Errorf("cookie %q lacks %s", cookie, flag)
		}
	}
	if got := a.call("GET", "/me", nil, tokenOf(cookie)).Status; got != 200 {
		t.Fatalf("/me answered %d", got)
	}
}

func TestTheAnswerIsTheSameForTheRightNumberAndAWrongOne(t *testing.T) {
	a := newApp(t)
	right := a.call("POST", "/auth/start", map[string]string{"phone": testenv.OwnerPhone}, "")
	a.Advance((auth.ResendSeconds + 1) * time.Second)
	wrong := a.call("POST", "/auth/start", map[string]string{"phone": "+61400000099"}, "")
	for _, r := range []reply{right, wrong} {
		if r.Status != 202 || r.body["sent"] != true || len(r.Cookies) != 0 || len(r.body) != 1 {
			t.Fatalf("%d %v %v", r.Status, r.body, r.Cookies)
		}
	}
	if n := len(a.SNS.Sent()); n != 1 {
		t.Fatalf("%d messages, want 1", n)
	}
}

func TestThePageCanSignInWithTheNumberAndTheCode(t *testing.T) {
	a := newApp(t)
	if got := a.call("GET", "/me", nil, "").Status; got != 401 {
		t.Fatalf("/me answered %d without a session", got)
	}
	a.call("POST", "/auth/start", map[string]string{"phone": "+61 400 000 001"}, "")
	r := a.call("POST", "/auth/verify", map[string]string{"otp": a.SNS.LastCode()}, "")
	if r.Status != 200 || len(r.Cookies) == 0 {
		t.Fatalf("%d %v", r.Status, r.body)
	}
}

func TestARequestThatIsNotANumberIsA400(t *testing.T) {
	a := newApp(t)
	for _, body := range []map[string]any{{}, {"phone": ""}, {"phone": "hello"}, {"email": "ann@confluent.io"}, {"phone": 61400000001}} {
		r := a.call("POST", "/auth/start", body, "")
		if r.Status != 400 || errorCode(r) != "invalid_number" {
			t.Errorf("%v: %d %v", body, r.Status, r.body)
		}
	}
	if len(a.SNS.Sent()) != 0 {
		t.Fatal("a message was sent")
	}
}

func TestTheAdminRoutesAreGone(t *testing.T) {
	a := newApp(t)
	token := a.token()
	for _, c := range [][2]string{{"GET", "/admin/requests"}, {"GET", "/admin/users"}, {"POST", "/admin/users/ann@confluent.io/approve"}} {
		if got := a.call(c[0], c[1], nil, token).Status; got != 404 {
			t.Errorf("%s %s answered %d", c[0], c[1], got)
		}
	}
}

func TestMutationsNeedTheCSRFHeader(t *testing.T) {
	a := newApp(t)
	token := a.token()
	body := map[string]string{"apiKey": "KEY12345", "apiSecret": "secretsecret"}
	if got := a.call("PUT", "/credentials", body, token, "x-launchpad", "0").Status; got != 403 {
		t.Fatalf("answered %d with the wrong header", got)
	}
	if got := a.send(&api.Request{Method: "PUT", Path: "/credentials", Cookies: map[string]string{api.Cookie: token}}).Status; got != 403 {
		t.Fatalf("answered %d with no header", got)
	}
	if got := a.call("GET", "/credentials", nil, token).body; got["configured"] != false {
		t.Fatalf("credentials %v", got)
	}
}

func TestCredentialsFlowNeverReturnsTheSecret(t *testing.T) {
	a := newApp(t)
	token := a.token()
	r := a.call("PUT", "/credentials", map[string]string{"apiKey": "KEY12345", "apiSecret": "topsecretvalue"}, token)
	if r.body["configured"] != true || r.body["apiKeyHint"] != "2345" {
		t.Fatalf("%v", r.body)
	}
	me := a.call("GET", "/me", nil, token)
	encoded, _ := json.Marshal(me.body)
	if strings.Contains(string(encoded), "topsecretvalue") {
		t.Fatal("the secret is in /me")
	}
	creds, _ := me.body["credentials"].(map[string]any)
	if creds["configured"] != true {
		t.Fatalf("/me credentials %v", me.body)
	}
	if got := a.call("DELETE", "/credentials", nil, token).body; got["configured"] != false || len(got) != 1 {
		t.Fatalf("%v", got)
	}
	if r := a.call("PUT", "/credentials", map[string]string{"apiKey": "k k", "apiSecret": "x"}, token); r.Status != 400 || errorCode(r) != "invalid_credentials" {
		t.Fatalf("%d %v", r.Status, r.body)
	}
}

func TestDemoListLaunchAndView(t *testing.T) {
	a := newApp(t)
	token := a.token()
	list := a.call("GET", "/demos", nil, token)
	demos, _ := list.body["demos"].([]any)
	byID := map[string]map[string]any{}
	for _, d := range demos {
		m := d.(map[string]any)
		byID[m["id"].(string)] = m
	}
	if len(byID) != 2 || byID["pims"]["needsConfluent"] != true || byID["plain"]["state"] != nil {
		t.Fatalf("demos %v", list.body)
	}
	// What the page reads: exactly these fields, and a list for the problems even when there are none.
	for _, field := range []string{"id", "name", "description", "valid", "problems", "needsConfluent", "state"} {
		if _, ok := byID["plain"][field]; !ok {
			t.Errorf("a demo has no %q", field)
		}
	}
	if problems, ok := byID["plain"]["problems"].([]any); !ok || len(problems) != 0 {
		t.Errorf("problems %v", byID["plain"]["problems"])
	}

	if r := a.call("POST", "/demos/pims/launch", nil, token); r.Status != 409 || errorCode(r) != "credentials_required" {
		t.Fatalf("%d %v", r.Status, r.body)
	}
	r := a.call("POST", "/demos/plain/launch", nil, token)
	state, _ := r.body["state"].(map[string]any)
	if r.Status != 200 || state["status"] != "launching" || state["canTearDown"] != true {
		t.Fatalf("%d %v", r.Status, r.body)
	}
	if _, ok := state["logTail"]; !ok {
		t.Error("the demo's own view must carry the log")
	}
	for _, field := range []string{"status", "url", "error", "logTail", "launchedBy", "updatedAt", "ref", "canTearDown"} {
		if _, ok := state[field]; !ok {
			t.Errorf("the state has no %q", field)
		}
	}

	listed := a.call("GET", "/demos", nil, token)
	for _, d := range listed.body["demos"].([]any) {
		m := d.(map[string]any)
		if m["id"] == "plain" {
			if _, ok := m["state"].(map[string]any)["logTail"]; ok {
				t.Error("the list must not carry the log")
			}
		}
	}
	if got := a.call("GET", "/demos/plain", nil, token).body["state"].(map[string]any); got["logTail"] == nil {
		t.Errorf("the demo has no log: %v", got)
	}
	if got := a.call("GET", "/demos/nope", nil, token).Status; got != 404 {
		t.Fatalf("an unknown demo answered %d", got)
	}
	if got := a.call("POST", "/demos/refresh", nil, token).Status; got != 200 {
		t.Fatalf("refresh answered %d", got)
	}
	if got := a.call("POST", "/demos/plain/teardown", nil, token).Status; got != 409 {
		t.Fatalf("teardown of a launching demo answered %d", got)
	}
}

func TestLogoutClearsTheCookieAndTheSession(t *testing.T) {
	a := newApp(t)
	token := a.token()
	r := a.call("POST", "/auth/logout", nil, token)
	if len(r.Cookies) != 1 || !strings.Contains(r.Cookies[0], "Max-Age=0") || r.body["ok"] != true {
		t.Fatalf("%v %v", r.Cookies, r.body)
	}
	if got := a.call("GET", "/me", nil, token).Status; got != 401 {
		t.Fatalf("/me answered %d after logout", got)
	}
}

func TestRoutingErrorsAndBadBodies(t *testing.T) {
	a := newApp(t)
	if got := a.call("GET", "/nope", nil, "").Status; got != 404 {
		t.Errorf("unknown path: %d", got)
	}
	if got := a.call("DELETE", "/me", nil, "").Status; got != 405 {
		t.Errorf("wrong method: %d", got)
	}
	post := func(body string) reply {
		return a.send(&api.Request{Method: "POST", Path: "/auth/start", Headers: map[string]string{"x-launchpad": "1"}, Body: []byte(body)})
	}
	if r := post("{not json"); r.Status != 400 || errorCode(r) != "invalid_json" {
		t.Errorf("bad JSON: %d %v", r.Status, r.body)
	}
	for _, body := range []string{"[]", "null", "5", `"x"`} {
		if r := post(body); r.Status != 400 || errorCode(r) != "invalid_json" {
			t.Errorf("%s: %d %v", body, r.Status, r.body)
		}
	}
	if r := post(strings.Repeat("x", 20000)); r.Status != 413 {
		t.Errorf("a large body: %d", r.Status)
	}
}

type queriesFail struct{ db.DB }

func (queriesFail) Query(context.Context, string, string) ([]db.Item, error) {
	return nil, errors.New("secret detail: table arn:aws:dynamodb:...")
}

func TestUnexpectedErrorsDoNotLeak(t *testing.T) {
	a := newApp(t)
	token := a.token()
	a.call("GET", "/demos", nil, token) // fills the catalog cache
	a.DB = queriesFail{a.Table}
	r := a.call("GET", "/demos", nil, token)
	if r.Status != 500 || r.body["error"].(map[string]any)["message"] != "Something went wrong" {
		t.Fatalf("%d %v", r.Status, r.body)
	}
	if !strings.Contains(a.Logs(), "secret detail") {
		t.Fatal("the cause is not in the log")
	}
}

func TestLambdaEventConversion(t *testing.T) {
	event := events.APIGatewayV2HTTPRequest{
		RawPath:         "/api/credentials/",
		Cookies:         []string{"lp_session=abc", "other=1"},
		Headers:         map[string]string{"X-Launchpad": "1"},
		RequestContext:  events.APIGatewayV2HTTPRequestContext{HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "put"}},
		Body:            base64.StdEncoding.EncodeToString([]byte(`{"a":1}`)),
		IsBase64Encoded: true,
	}
	req, err := api.FromEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "PUT" || req.Path != "/credentials" || req.Token() != "abc" || req.Headers["x-launchpad"] != "1" || string(req.Body) != `{"a":1}` {
		t.Fatalf("%+v", req)
	}
}

func TestLambdaEventPaths(t *testing.T) {
	for raw, want := range map[string]string{"/api/me": "/me", "/api/demos/pims/launch": "/demos/pims/launch", "": "/", "/api": "/api", "/api/": "/"} {
		req, err := api.FromEvent(events.APIGatewayV2HTTPRequest{RawPath: raw})
		if err != nil || req.Path != want {
			t.Errorf("%q became %q, %v; want %q", raw, req.Path, err, want)
		}
	}
	if _, err := api.FromEvent(events.APIGatewayV2HTTPRequest{Body: "!!", IsBase64Encoded: true}); err == nil {
		t.Error("a body that is not base64 was accepted")
	}
	plain, _ := api.FromEvent(events.APIGatewayV2HTTPRequest{Body: `{"a":1}`})
	if string(plain.Body) != `{"a":1}` {
		t.Errorf("body %q", plain.Body)
	}
}

func TestLambdaResponse(t *testing.T) {
	out := api.ToEvent(api.Response{Status: 200, Body: map[string]bool{"ok": true}, Cookies: []string{"a=b"}})
	if out.StatusCode != 200 || out.Body != `{"ok":true}` || out.Headers["content-type"] != "application/json" || out.Headers["cache-control"] != "no-store" || len(out.Cookies) != 1 {
		t.Fatalf("%+v", out)
	}
	if empty := api.ToEvent(api.Response{Status: 204}); empty.Body != "{}" || len(empty.Cookies) != 0 {
		t.Fatalf("%+v", empty)
	}
	if broken := api.ToEvent(api.Response{Status: 200, Body: make(chan int)}); broken.StatusCode != 500 {
		t.Fatalf("an unencodable body answered %d", broken.StatusCode)
	}
}

type panicsOnQuery struct{ db.DB }

func (panicsOnQuery) Query(context.Context, string, string) ([]db.Item, error) {
	panic("index out of range")
}

func TestAPanicIsAJSON500(t *testing.T) {
	a := newApp(t)
	token := a.token()
	a.call("GET", "/demos", nil, token) // fills the catalog cache
	a.DB = panicsOnQuery{a.Table}
	r := a.call("GET", "/demos", nil, token)
	if r.Status != 500 || errorCode(r) != "internal" || r.body["error"].(map[string]any)["message"] != "Something went wrong" {
		t.Fatalf("%d %v", r.Status, r.body)
	}
}

func TestAnUpdateIsOfferedForANewCommitAndAppliedOnRequest(t *testing.T) {
	a := newApp(t)
	token := a.token()
	if r := a.call("POST", "/demos/plain/update", nil, ""); r.Status != 401 {
		t.Fatalf("an update without a session answered %d", r.Status)
	}
	if r := a.call("POST", "/demos/plain/update", nil, token); r.Status != 404 {
		t.Fatalf("an update of a demo that is not deployed answered %d", r.Status)
	}
	if r := a.call("POST", "/demos/plain/launch", nil, token); r.Status != 200 {
		t.Fatalf("launch answered %d", r.Status)
	}
	run, err := runs.GetRaw(ctx, a.Services, "plain")
	if err != nil || run == nil {
		t.Fatalf("%v, %v", run, err)
	}
	if ok, err := runs.Finish(ctx, a.Services, "plain", run.RunID, runs.Running, runs.Outcome{URL: "http://x"}); !ok || err != nil {
		t.Fatalf("%v, %v", ok, err)
	}
	stateOf := func(r reply) map[string]any {
		t.Helper()
		m, _ := r.body["state"].(map[string]any)
		if m == nil {
			t.Fatalf("%d %v", r.Status, r.body)
		}
		return m
	}
	if got := stateOf(a.call("GET", "/demos/plain", nil, token)); got["updateAvailable"] != false || got["canUpdate"] != true {
		t.Fatalf("state %v", got)
	}
	if r := a.call("POST", "/demos/plain/update", nil, token); r.Status != 409 || errorCode(r) != "up_to_date" {
		t.Fatalf("%d %v", r.Status, r.body)
	}

	const newSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	a.GitHub.AddRepoAt("owner/repo", newSHA, map[string]string{"demos/pims/tf/main.tf": testenv.CloudTF, "demos/plain/tf/main.tf": testenv.GoodTF + "# changed\n"})
	if r := a.call("POST", "/demos/refresh", nil, token); r.Status != 200 {
		t.Fatalf("refresh answered %d", r.Status)
	}
	if got := stateOf(a.call("GET", "/demos/plain", nil, token)); got["updateAvailable"] != true {
		t.Fatalf("state %v", got)
	}
	r := a.call("POST", "/demos/plain/update", nil, token)
	got := stateOf(r)
	if r.Status != 200 || got["status"] != "updating" || got["ref"] != newSHA || got["updateAvailable"] != false {
		t.Fatalf("%d %v", r.Status, r.body)
	}
	if r := a.call("POST", "/demos/plain/update", nil, token); r.Status != 409 || errorCode(r) != "not_updatable" {
		t.Fatalf("%d %v", r.Status, r.body)
	}
	if r := a.call("POST", "/demos/plain/update", nil, token, "x-launchpad", "0"); r.Status != 403 {
		t.Fatalf("an update without the CSRF header answered %d", r.Status)
	}
}
