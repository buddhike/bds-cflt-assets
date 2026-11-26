// Package api is the HTTP API: routing, sessions and JSON, with no AWS event format in it.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"launchpad/internal/apperr"
	"launchpad/internal/auth"
	"launchpad/internal/catalog"
	"launchpad/internal/credentials"
	"launchpad/internal/runs"
	"launchpad/internal/services"
	"launchpad/internal/users"
)

const (
	Cookie  = "lp_session"
	MaxBody = 10_000
)

// Request is an HTTP request with the /api prefix already removed.
type Request struct {
	Method  string
	Path    string
	Headers map[string]string // lower-case names
	Cookies map[string]string
	Body    []byte
}

// JSON decodes the body as a JSON object. An empty body is an empty object.
func (r *Request) JSON() (map[string]any, error) {
	if len(r.Body) > MaxBody {
		return nil, apperr.New(413, "too_large", "The request is too large")
	}
	if len(r.Body) == 0 {
		return map[string]any{}, nil
	}
	var data any
	if err := json.Unmarshal(r.Body, &data); err != nil {
		return nil, apperr.BadRequest("invalid_json", "The request is not valid JSON")
	}
	object, ok := data.(map[string]any)
	if !ok {
		return nil, apperr.BadRequest("invalid_json", "The request must be a JSON object")
	}
	return object, nil
}

// Token is the session token the request carries, or "".
func (r *Request) Token() string { return r.Cookies[Cookie] }

// Response is what a handler answers. Body is encoded as JSON.
type Response struct {
	Status  int
	Body    any
	Cookies []string
}

// SessionCookie is the Set-Cookie value that holds a session.
func SessionCookie(token string, maxAge int) string {
	return fmt.Sprintf("%s=%s; Max-Age=%d; Path=/api; HttpOnly; Secure; SameSite=Strict", Cookie, token, maxAge)
}

type handler func(ctx context.Context, svc *services.Services, req *Request, params map[string]string) (Response, error)

type route struct {
	method  string
	pattern *regexp.Regexp
	handle  handler
}

var routes = []route{
	newRoute("POST", "/auth/start", startSignIn),
	newRoute("POST", "/auth/verify", verifySignIn),
	newRoute("POST", "/auth/logout", signOut),
	newRoute("GET", "/me", me),
	newRoute("GET", "/credentials", getCredentials),
	newRoute("PUT", "/credentials", putCredentials),
	newRoute("DELETE", "/credentials", deleteCredentials),
	newRoute("GET", "/demos", listDemos),
	newRoute("POST", "/demos/refresh", refreshDemos),
	newRoute("GET", "/demos/{id}", getDemo),
	newRoute("POST", "/demos/{id}/launch", launch),
	newRoute("POST", "/demos/{id}/update", update),
	newRoute("POST", "/demos/{id}/teardown", teardown),
}

var placeholder = regexp.MustCompile(`\{(\w+)\}`)

func newRoute(method, pattern string, h handler) route {
	expr := placeholder.ReplaceAllString(pattern, `(?P<$1>[^/]+)`)
	return route{method: method, pattern: regexp.MustCompile("^" + expr + "$"), handle: h}
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func errorResponse(status int, code, message string) Response {
	var body errorBody
	body.Error.Code, body.Error.Message = code, message
	return Response{Status: status, Body: body}
}

// Dispatch runs a request. Failures become JSON error responses.
func Dispatch(ctx context.Context, svc *services.Services, req *Request) Response {
	resp, err := safeDispatch(ctx, svc, req)
	if err == nil {
		return resp
	}
	if appErr, ok := apperr.As(err); ok {
		return errorResponse(appErr.Status, appErr.Code, appErr.Message)
	}
	svc.Log.Error("Unhandled error", "method", req.Method, "path", req.Path, "error", err)
	return errorResponse(http.StatusInternalServerError, "internal", "Something went wrong")
}

// safeDispatch is dispatch with a panic turned into an error, which answers as a 500 with the usual body.
func safeDispatch(ctx context.Context, svc *services.Services, req *Request) (resp Response, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return dispatch(ctx, svc, req)
}

func dispatch(ctx context.Context, svc *services.Services, req *Request) (Response, error) {
	pathKnown := false
	for _, r := range routes {
		m := r.pattern.FindStringSubmatch(req.Path)
		if m == nil {
			continue
		}
		pathKnown = true
		if r.method != req.Method {
			continue
		}
		// SameSite=Strict already keeps other sites from sending the cookie. Requiring a header
		// a form cannot set is a second, independent guard.
		if req.Method != http.MethodGet && req.Headers["x-launchpad"] != "1" {
			return Response{}, apperr.Forbidden("Missing request header")
		}
		params := map[string]string{}
		for i, name := range r.pattern.SubexpNames() {
			if name != "" {
				params[name] = m[i]
			}
		}
		return r.handle(ctx, svc, req, params)
	}
	if pathKnown {
		return Response{}, apperr.New(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
	return Response{}, apperr.NotFound()
}

func requireUser(ctx context.Context, svc *services.Services, req *Request) (*users.User, error) {
	return auth.SessionUser(ctx, svc, req.Token())
}

func requireAdmin(ctx context.Context, svc *services.Services, req *Request) (*users.User, error) {
	user, err := requireUser(ctx, svc, req)
	if err != nil {
		return nil, err
	}
	if user.Role != users.Admin {
		return nil, apperr.Forbidden("Only admins can do this")
	}
	return user, nil
}

// --- sign in -------------------------------------------------------------

func startSignIn(ctx context.Context, svc *services.Services, req *Request, _ map[string]string) (Response, error) {
	body, err := req.JSON()
	if err != nil {
		return Response{}, err
	}
	if err := auth.Start(ctx, svc, body["phone"]); err != nil {
		return Response{}, err
	}
	return Response{Status: 202, Body: map[string]bool{"sent": true}}, nil // always: see auth.Start
}

func verifySignIn(ctx context.Context, svc *services.Services, req *Request, _ map[string]string) (Response, error) {
	body, err := req.JSON()
	if err != nil {
		return Response{}, err
	}
	result, err := auth.Verify(ctx, svc, body["otp"])
	if err != nil {
		return Response{}, err
	}
	return Response{
		Status: 200,
		Body: struct {
			User *users.User `json:"user"`
		}{result.User},
		Cookies: []string{SessionCookie(result.Token, svc.Settings.SessionHours*3600)},
	}, nil
}

func signOut(ctx context.Context, svc *services.Services, req *Request, _ map[string]string) (Response, error) {
	if err := auth.Logout(ctx, svc, req.Token()); err != nil {
		return Response{}, err
	}
	return Response{Status: 200, Body: map[string]bool{"ok": true}, Cookies: []string{SessionCookie("", 0)}}, nil
}

func me(ctx context.Context, svc *services.Services, req *Request, _ map[string]string) (Response, error) {
	user, err := requireUser(ctx, svc, req)
	if err != nil {
		return Response{}, err
	}
	status, err := credentials.GetStatus(ctx, svc, user.Email)
	if err != nil {
		return Response{}, err
	}
	return Response{Status: 200, Body: struct {
		*users.User
		Credentials credentials.Status `json:"credentials"`
	}{user, status}}, nil
}

// --- credentials ---------------------------------------------------------

func getCredentials(ctx context.Context, svc *services.Services, req *Request, _ map[string]string) (Response, error) {
	user, err := requireUser(ctx, svc, req)
	if err != nil {
		return Response{}, err
	}
	status, err := credentials.GetStatus(ctx, svc, user.Email)
	return Response{Status: 200, Body: status}, err
}

func putCredentials(ctx context.Context, svc *services.Services, req *Request, _ map[string]string) (Response, error) {
	user, err := requireUser(ctx, svc, req)
	if err != nil {
		return Response{}, err
	}
	body, err := req.JSON()
	if err != nil {
		return Response{}, err
	}
	status, err := credentials.Save(ctx, svc, user.Email, body["apiKey"], body["apiSecret"])
	return Response{Status: 200, Body: status}, err
}

func deleteCredentials(ctx context.Context, svc *services.Services, req *Request, _ map[string]string) (Response, error) {
	user, err := requireUser(ctx, svc, req)
	if err != nil {
		return Response{}, err
	}
	if err := credentials.Delete(ctx, svc, user.Email); err != nil {
		return Response{}, err
	}
	return Response{Status: 200, Body: credentials.Status{Configured: false}}, nil
}

// --- demos ---------------------------------------------------------------

type stateView struct {
	Status      string  `json:"status"`
	URL         string  `json:"url"`
	Error       string  `json:"error"`
	LogTail     *string `json:"logTail,omitempty"`
	LaunchedBy  string  `json:"launchedBy"`
	UpdatedAt   int64   `json:"updatedAt"`
	Ref         string  `json:"ref"`
	CanTearDown bool    `json:"canTearDown"`
	// CanUpdate is whether the user may update the demo. It is who may tear it down.
	CanUpdate bool `json:"canUpdate"`
	// UpdateAvailable is whether a running demo is at an older commit than the catalog's.
	UpdateAvailable bool `json:"updateAvailable"`
}

type demoView struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Ref            string     `json:"ref"`
	Latest         bool       `json:"latest"`
	Valid          bool       `json:"valid"`
	Problems       []string   `json:"problems"`
	NeedsConfluent bool       `json:"needsConfluent"`
	State          *stateView `json:"state"`
}

func newDemoView(user *users.User, demo catalog.Demo, state *runs.Public, withLog bool) demoView {
	view := demoView{
		ID: demo.ID, Name: demo.Name, Description: demo.Description, Ref: demo.Ref, Latest: demo.Latest, Valid: demo.Valid,
		Problems: demo.Problems, NeedsConfluent: demo.NeedsConfluent,
	}
	if view.Problems == nil {
		view.Problems = []string{}
	}
	if state != nil {
		view.State = &stateView{
			Status: state.Status, URL: state.URL, Error: state.Error, LaunchedBy: state.LaunchedBy,
			UpdatedAt: state.UpdatedAt, Ref: state.Ref,
			CanTearDown: user.Role == users.Admin || user.Email == state.LaunchedBy,
		}
		view.State.CanUpdate = view.State.CanTearDown
		view.State.UpdateAvailable = state.Status == runs.Running && demo.Valid && state.Ref != demo.Ref
		if withLog {
			view.State.LogTail = &state.LogTail
		}
	}
	return view
}

func demoList(ctx context.Context, svc *services.Services, user *users.User, force bool) (Response, error) {
	demos, err := catalog.List(ctx, svc, force)
	if err != nil {
		return Response{}, err
	}
	ids := make([]string, len(demos))
	for i, d := range demos {
		ids[i] = d.ID
	}
	states, err := runs.StatesFor(ctx, svc, ids)
	if err != nil {
		return Response{}, err
	}
	views := make([]demoView, len(demos))
	for i, d := range demos {
		views[i] = newDemoView(user, d, states[d.ID], false)
	}
	return Response{Status: 200, Body: map[string][]demoView{"demos": views}}, nil
}

func listDemos(ctx context.Context, svc *services.Services, req *Request, _ map[string]string) (Response, error) {
	user, err := requireUser(ctx, svc, req)
	if err != nil {
		return Response{}, err
	}
	return demoList(ctx, svc, user, false)
}

func refreshDemos(ctx context.Context, svc *services.Services, req *Request, _ map[string]string) (Response, error) {
	user, err := requireAdmin(ctx, svc, req)
	if err != nil {
		return Response{}, err
	}
	return demoList(ctx, svc, user, true)
}

func demoResponse(ctx context.Context, svc *services.Services, user *users.User, id string) (Response, error) {
	demo, err := catalog.Get(ctx, svc, id)
	if err != nil {
		return Response{}, err
	}
	if demo == nil {
		return Response{}, apperr.NotFound("No such demo")
	}
	state, err := runs.GetRaw(ctx, svc, demo.ID)
	if err != nil {
		return Response{}, err
	}
	return Response{Status: 200, Body: newDemoView(user, *demo, state.Public(), true)}, nil
}

func getDemo(ctx context.Context, svc *services.Services, req *Request, params map[string]string) (Response, error) {
	user, err := requireUser(ctx, svc, req)
	if err != nil {
		return Response{}, err
	}
	return demoResponse(ctx, svc, user, params["id"])
}

func launch(ctx context.Context, svc *services.Services, req *Request, params map[string]string) (Response, error) {
	user, err := requireUser(ctx, svc, req)
	if err != nil {
		return Response{}, err
	}
	if _, err := runs.RequestLaunch(ctx, svc, params["id"], user.Email); err != nil {
		return Response{}, err
	}
	return demoResponse(ctx, svc, user, params["id"])
}

func teardown(ctx context.Context, svc *services.Services, req *Request, params map[string]string) (Response, error) {
	user, err := requireUser(ctx, svc, req)
	if err != nil {
		return Response{}, err
	}
	if _, err := runs.RequestTeardown(ctx, svc, params["id"], user); err != nil {
		return Response{}, err
	}
	return demoResponse(ctx, svc, user, params["id"])
}

func update(ctx context.Context, svc *services.Services, req *Request, params map[string]string) (Response, error) {
	user, err := requireUser(ctx, svc, req)
	if err != nil {
		return Response{}, err
	}
	if _, err := runs.RequestUpdate(ctx, svc, params["id"], user); err != nil {
		return Response{}, err
	}
	return demoResponse(ctx, svc, user, params["id"])
}
