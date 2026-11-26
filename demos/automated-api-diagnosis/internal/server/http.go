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
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"apidiag/internal/simulator"
)

const (
	// CookieName holds the browser's proof that it was given the access token.
	CookieName = "apidiag_access"
	// AccessParam is the query parameter the Launchpad link carries the token in.
	AccessParam = "access"
	// CSRFHeader must be on every request that changes something.
	CSRFHeader     = "X-APIDIAG"
	maxBody        = 1 << 20
	heartbeatEvery = 15 * time.Second
	// writeTimeout bounds one write to an event stream, so a stalled peer cannot hold a goroutine.
	writeTimeout = 30 * time.Second
)

// Handler serves the API and the UI in assets (the built Svelte app).
func (s *Service) Handler(assets fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.State())
	})
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/apis", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.APIs())
	})
	mux.HandleFunc("POST /api/simulation/start", s.handleStart)
	mux.HandleFunc("POST /api/simulation/stop", func(w http.ResponseWriter, _ *http.Request) {
		s.StopSimulation()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/apis/update", s.handleUpdate)
	mux.HandleFunc("GET /api/health/backlog", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.HealthBacklog())
	})
	mux.HandleFunc("GET /api/reports/backlog", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.ReportBacklog())
	})
	// An unknown API path is an error, not the app's page: a mistyped endpoint should not
	// fail as a JSON parse error in the browser.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, errors.New("no such endpoint"))
	})
	mux.Handle("/", staticHandler(assets))

	gate := s.gate(mux)
	root := http.NewServeMux()
	// The load balancer's health check carries no token.
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	root.Handle("/", securityHeaders(gate))
	return root
}

// cookieValue is what the cookie holds: not the token, so the cookie is not a copy of the link.
func cookieValue(token string) string {
	sum := sha256.Sum256([]byte("apidiag-session:" + token))
	return hex.EncodeToString(sum[:])
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// gate lets a request through only if it carries the access cookie. A request with the
// token in its address is given the cookie and sent to the same page without the token,
// so the token does not stay in the address bar, history or a Referer header.
func (s *Service) gate(next http.Handler) http.Handler {
	want := cookieValue(s.cfg.AccessToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(CookieName); err == nil && equal(c.Value, want) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(CSRFHeader) != "1" {
				writeError(w, http.StatusForbidden, errors.New("missing request header"))
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if token := r.URL.Query().Get(AccessParam); token != "" && equal(token, s.cfg.AccessToken) && r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{
				Name:     CookieName,
				Value:    want,
				Path:     "/",
				HttpOnly: true,
				// Lax, not Strict: the first visit is a click from another site (Launchpad), and a
				// redirect chain that began there is cross-site, so a Strict cookie would not come
				// back on the redirected request. Everything that changes state is a non-GET and
				// needs the CSRF header as well.
				SameSite: http.SameSiteLaxMode,
				// Behind CloudFront the load balancer speaks plain HTTP, so the viewer's protocol
				// arrives in CloudFront-Forwarded-Proto. Where neither header says HTTPS (a laptop),
				// a Secure cookie would never come back.
				Secure: viaHTTPS(r),
				MaxAge: 7 * 24 * 3600,
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

// viaHTTPS reports whether the viewer used HTTPS, as the proxies in front say. A viewer
// that lies only makes its own cookie Secure or not.
func viaHTTPS(r *http.Request) bool {
	return r.Header.Get("CloudFront-Forwarded-Proto") == "https" || r.Header.Get("X-Forwarded-Proto") == "https"
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		// Svelte writes its component styles as style attributes, and the AI reports are
		// Markdown rendered into the page (after DOMPurify), so scripts stay 'self' only.
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

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("the request is not valid JSON"))
		return false
	}
	return true
}

func statusFor(err error) int {
	if errors.Is(err, ErrInvalid) {
		return http.StatusBadRequest
	}
	if errors.Is(err, ErrClosed) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// handleStart starts the simulation with the settings the page shows. The body may be
// empty, which starts it with the settings the server has.
func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIs []simulator.APIConfig `json:"apis"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	if err := s.StartSimulation(body.APIs); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var cfg simulator.APIConfig
	if !decode(w, r, &cfg) {
		return
	}
	running, err := s.UpdateAPI(cfg)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"running": running})
}

// handleEvents streams server-sent events. A comment line every few seconds keeps the
// load balancer from closing a connection that has been quiet.
func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported"))
		return
	}
	events, cancel := s.hub.subscribe()
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "retry: 3000\n\n")
	f.Flush()

	rc := http.NewResponseController(w)
	beat := time.NewTicker(heartbeatEvery)
	defer beat.Stop()
	for {
		_ = rc.SetWriteDeadline(time.Now().Add(heartbeatEvery + writeTimeout)) // unsupported on some writers: then no deadline
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
		case e, open := <-events:
			if !open {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, e.data); err != nil {
				return
			}
		}
		f.Flush()
	}
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
			// Built file names carry a content hash.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
