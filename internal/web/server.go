// Package web serves jhh's dashboard: static files and a small JSON API
// over the core packages, on 127.0.0.1 only. It is a window onto the data
// the agent works with and has no logic of its own.
//
// Security (docs/design.md section 7): every request must name this
// server in Host and, when the browser sends one, Origin, which blocks DNS
// rebinding and other sites. The API needs a session cookie, which the
// browser gets by opening a one-time link from open_dashboard, and writes
// must be application/json, so a browser asks the server before sending
// one from anywhere else. A strict Content Security Policy allows only
// this server's own script and styles.
package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/keys"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

//go:embed static
var staticFiles embed.FS

const (
	sessionCookie = "jhh_session"
	loginTTL      = 15 * time.Minute    // how long an open_dashboard link works
	sessionTTL    = 30 * 24 * time.Hour // how long the browser stays signed in
	maxBody       = 256 << 10           // the largest request body: a pasted description fits

	csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
		"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
)

// Deps is what the dashboard works with.
type Deps struct {
	Store   *store.Store
	Logger  *slog.Logger
	Sources map[string]sources.Source // for adding a job by its link
	// Searchers check a key before it's saved.
	Searchers map[string]sources.Searcher
	// Keys holds services' optional keys. The dashboard is the only place
	// one is entered, and nothing here ever sends one back.
	Keys *keys.File
	// Instance names the data directory, so a process can tell whether the
	// jhh serving the port shares its data.
	Instance string
}

// server handles the dashboard's requests for one listening port.
type server struct {
	Deps
	port   int
	hub    *hub
	static http.Handler
}

func newServer(d Deps, port int, h *hub) *server {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return &server{Deps: d, port: port, hub: h, static: http.FileServerFS(sub)}
}

// handler routes every request through the security checks.
func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.Handle("GET /app.js", s.static)
	mux.Handle("GET /app.css", s.static)
	mux.HandleFunc("GET /api/hello", s.hello)
	s.routes(mux)
	return s.guard(mux)
}

// guard applies the checks and headers every response needs.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		if !s.ownHost(r.Host) {
			http.Error(w, "jhh's dashboard answers only to 127.0.0.1 and localhost", http.StatusMisdirectedRequest)
			return
		}
		// Links to the page itself may come from anywhere (a chat app, say);
		// the API answers only the page.
		site := r.Header.Get("Sec-Fetch-Site")
		if o := r.Header.Get("Origin"); o != "" && !s.ownOrigin(o) ||
			strings.HasPrefix(r.URL.Path, "/api/") && site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "requests from other sites are refused", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				http.Error(w, "send application/json", http.StatusUnsupportedMediaType)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) ownHost(host string) bool {
	p := ":" + strconv.Itoa(s.port)
	return host == "127.0.0.1"+p || host == "localhost"+p
}

func (s *server) ownOrigin(origin string) bool {
	return s.ownHost(strings.TrimPrefix(origin, "http://")) && strings.HasPrefix(origin, "http://")
}

// index serves the page. A link from open_dashboard carries a one-time
// token in t: it is swapped for a session cookie, then the browser is sent
// to the plain address so the token leaves the address bar and history.
func (s *server) index(w http.ResponseWriter, r *http.Request) {
	if t := r.URL.Query().Get("t"); t != "" {
		ok, err := s.Store.UseLoginToken(r.Context(), hash(t))
		if err != nil {
			s.Logger.Error("checking a dashboard link", "err", err)
		}
		if ok {
			session := newToken()
			if err := s.Store.AddUIToken(r.Context(), store.TokenSession, hash(session), sessionTTL); err != nil {
				s.Logger.Error("starting a dashboard session", "err", err)
				http.Error(w, "jhh couldn't sign you in; see its log", http.StatusInternalServerError)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: session, Path: "/", MaxAge: int(sessionTTL.Seconds()),
				HttpOnly: true, SameSite: http.SameSiteStrictMode})
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.static.ServeHTTP(w, r)
}

// hello says this is jhh and which data it serves, so another jhh process
// can hand out links to it. It needs no session and reveals nothing else.
func (s *server) hello(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, helloJSON{App: "jhh", Instance: s.Instance})
}

type helloJSON struct {
	App      string `json:"app"`
	Instance string `json:"instance"`
}

// signedIn reports whether the request carries a live session cookie.
func (s *server) signedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	ok, err := s.Store.ValidSession(r.Context(), hash(c.Value))
	if err != nil {
		s.Logger.Error("checking a dashboard session", "err", err)
	}
	return ok
}

// newLoginLink stores a one-time token and returns the link that uses it.
func newLoginLink(ctx context.Context, st *store.Store, port int) (string, error) {
	t := newToken()
	if err := st.AddUIToken(ctx, store.TokenLogin, hash(t), loginTTL); err != nil {
		return "", err
	}
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/?t=" + t, nil
}

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b) // never fails (crypto/rand)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
