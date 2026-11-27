package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"runtime/debug"
	"strconv"
	"strings"

	"lightninginv/internal/protocol"
)

const (
	// CookieName holds the browser's proof that it was given the access token.
	CookieName = "lightning_access"
	// AccessParam is the query parameter the Launchpad link carries the token in.
	AccessParam = "access"
	// CSRFHeader must be on every request that changes something.
	CSRFHeader = "X-LIGHTNING"
	maxBody    = 1 << 20
)

// Handler serves the UI and API in assets (the built Svelte app) and the workers' heartbeat.
// There is no shutdown hook: Terraform owns everything the demo creates.
func (s *Service) Handler(assets fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.State()) })
	mux.HandleFunc("GET /api/latency", s.handleLatency)
	mux.HandleFunc("POST /api/events", s.handleLoad(s.ChangeEvents))
	mux.HandleFunc("POST /api/queries", s.handleLoad(s.ChangeQueries))
	mux.HandleFunc("POST /api/catalogue/load", func(w http.ResponseWriter, _ *http.Request) {
		if err := s.StartSeed(); err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	// An unknown API path is an error, not the app's page.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, errors.New("no such endpoint"))
	})
	mux.Handle("/", staticHandler(assets))

	root := http.NewServeMux()
	// The load balancer's health check carries no token.
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	// Workers: the worker token, and never through CloudFront, which people use.
	root.HandleFunc("POST "+protocol.HeartbeatPath, s.handleHeartbeat)
	root.Handle("/", securityHeaders(s.gate(mux)))
	return recoverer(root)
}

// recoverer turns a panic in a handler into a 500, logged without the request's address or
// headers (they carry the access token), so one bad request cannot end the connection
// without an answer or leave the cause unlogged. If the response has already started there
// is no status left to change and a body written now would be appended to a half-sent one:
// the connection is aborted instead.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &startedWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				slog.Error("A request handler panicked", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
				if sw.started {
					panic(http.ErrAbortHandler)
				}
				writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// startedWriter notes whether the response has begun.
type startedWriter struct {
	http.ResponseWriter
	started bool
}

func (w *startedWriter) WriteHeader(code int) {
	w.started = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *startedWriter) Write(b []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer (flushing, deadlines).
func (w *startedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// bearer reports whether the request carries token as a bearer token.
func bearer(r *http.Request, token string) bool {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	return len(h) > len(p) && strings.EqualFold(h[:len(p)], p) && equal(h[len(p):], token)
}

// viaCloudFront reports whether the request came through CloudFront (people) rather than
// straight to the load balancer (workers).
func viaCloudFront(r *http.Request) bool {
	return r.Header.Get("X-Amz-Cf-Id") != "" || r.Header.Get("CloudFront-Forwarded-Proto") != ""
}

func (s *Service) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if viaCloudFront(r) || !bearer(r, s.o.WorkerToken) {
		writeError(w, http.StatusUnauthorized, errors.New("not allowed"))
		return
	}
	var hb protocol.Heartbeat
	// Lenient about unknown fields, for this endpoint only: during a rolling deploy a worker
	// of the next version may send a field this control does not know yet. Everything else
	// (trailing data, bounds in Heartbeat.Valid) is as strict as elsewhere.
	if !decodeJSON(w, r, &hb, false) {
		return
	}
	plan, err := s.Heartbeat(hb)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Service) handleLatency(w http.ResponseWriter, r *http.Request) {
	minutes, err := strconv.Atoi(r.URL.Query().Get("minutes"))
	if err != nil {
		minutes = 5
	}
	series, err := s.Latency(r.Context(), minutes)
	if err != nil {
		code := statusFor(err)
		if r.Context().Err() != nil {
			return // the viewer went away
		}
		if code == http.StatusInternalServerError {
			slog.Warn("Could not read the latency metrics", "error", err)
			err = errors.New("could not read the latency metrics from CloudWatch")
			code = http.StatusBadGateway
		}
		writeError(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": series, "now": s.o.Now().Unix()})
}

func (s *Service) handleLoad(apply func(LoadChange) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var c LoadChange
		if !decode(w, r, &c) {
			return
		}
		if err := apply(c); err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, s.State())
	}
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// cookieValue is what the cookie holds: not the token, so the cookie is not a copy of the link.
func cookieValue(token string) string {
	sum := sha256.Sum256([]byte("lightning-session:" + token))
	return hex.EncodeToString(sum[:])
}

// gate lets a request through only if it carries the access cookie. A request with the
// token in its address is given the cookie and sent to the same page without the token.
func (s *Service) gate(next http.Handler) http.Handler {
	want := cookieValue(s.o.AccessToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(CookieName); err == nil && equal(c.Value, want) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(CSRFHeader) != "1" {
				writeError(w, http.StatusForbidden, errors.New("missing request header"))
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if token := r.URL.Query().Get(AccessParam); token != "" && equal(token, s.o.AccessToken) && r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{
				Name: CookieName, Value: want, Path: "/", HttpOnly: true,
				// Lax: the first visit is a click from Launchpad, a cross-site redirect chain.
				// Every state change is a non-GET and needs the header as well.
				SameSite: http.SameSiteLaxMode,
				Secure:   viaHTTPS(r),
				MaxAge:   7 * 24 * 3600,
			})
			q := r.URL.Query()
			q.Del(AccessParam)
			dest := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}
			http.Redirect(w, r, dest.String(), http.StatusSeeOther)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusUnauthorized, errors.New("open the demo from the link Launchpad shows"))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "This demo needs the link Launchpad shows, which carries its access token.\n")
	})
}

// viaHTTPS reports whether the viewer used HTTPS, as the proxies in front say.
func viaHTTPS(r *http.Request) bool {
	return r.Header.Get("CloudFront-Forwarded-Proto") == "https" || r.Header.Get("X-Forwarded-Proto") == "https"
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// decode reads one JSON value into v. Unknown fields (unless strict is false) and anything
// after the value are refused: the browser and the workers have a fixed vocabulary, and an extra field is a
// mistake or an attempt to set what is not theirs to set.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSON(w, r, v, true)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any, strict bool) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("the request is not valid JSON of the expected shape"))
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, errors.New("the request has data after the JSON value"))
		return false
	}
	return true
}

// staticHandler serves the built UI. A path that is not a file is the app's own page.
func staticHandler(assets fs.FS) http.Handler {
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if st, err := fs.Stat(assets, name); err != nil || st.IsDir() {
			r.URL.Path = "/"
		} else if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
