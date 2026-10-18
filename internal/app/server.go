package app

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is the HTTP application. One instance per process; it owns the session
// table and the login throttle.
type Client struct {
	store   *Store
	cfg     Config
	http    *http.Client
	log     *log.Logger
	mux     *http.ServeMux
	assets  fs.FS
	session *sessionTable
	limiter *loginLimiter
}

// NewServer builds the API handler. assets may be nil (API-only tests, or a
// deployment without a built frontend); when non-nil it serves "/".
// The returned handler owns no background goroutines: sessions and the login
// throttle are pruned opportunistically on each relevant request.
func NewServer(store *Store, cfg Config, assets fs.FS) (http.Handler, error) {
	return newServer(store, cfg, assets, log.Default())
}

func newServer(store *Store, cfg Config, assets fs.FS, logger *log.Logger) (*Client, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	c := &Client{
		store:   store,
		cfg:     cfg,
		http:    newModelClient(),
		log:     logger,
		mux:     http.NewServeMux(),
		assets:  assets,
		session: newSessionTable(cfg.SessionTTL),
		limiter: newLoginLimiter(cfg.LoginMaxFailures, cfg.LoginWindow),
	}
	c.routes()
	return c, nil
}

func (c *Client) routes() {
	// Session: readable without auth so the SPA knows whether to show the login
	// screen; every other /api route requires a session.
	c.mux.HandleFunc("GET /api/session", c.handleSessionGet)
	c.mux.HandleFunc("POST /api/session", c.requireOrigin(c.handleLogin))
	c.mux.HandleFunc("DELETE /api/session", c.requireOrigin(c.requireAuth(c.handleLogout)))

	c.mux.HandleFunc("GET /api/prompts", c.requireAuth(c.handlePromptList))
	c.mux.HandleFunc("POST /api/prompts", c.requireOrigin(c.requireAuth(c.handlePromptCreate)))
	c.mux.HandleFunc("GET /api/prompts/{id}", c.requireAuth(c.handlePromptGet))
	c.mux.HandleFunc("PUT /api/prompts/{id}", c.requireOrigin(c.requireAuth(c.handlePromptUpdate)))
	c.mux.HandleFunc("DELETE /api/prompts/{id}", c.requireOrigin(c.requireAuth(c.handlePromptDelete)))

	c.mux.HandleFunc("GET /api/schemes", c.requireAuth(c.handleSchemeList))
	c.mux.HandleFunc("POST /api/schemes", c.requireOrigin(c.requireAuth(c.handleSchemeCreate)))
	c.mux.HandleFunc("GET /api/schemes/{id}", c.requireAuth(c.handleSchemeGet))
	c.mux.HandleFunc("PUT /api/schemes/{id}", c.requireOrigin(c.requireAuth(c.handleSchemeUpdate)))
	c.mux.HandleFunc("DELETE /api/schemes/{id}", c.requireOrigin(c.requireAuth(c.handleSchemeDelete)))

	c.mux.HandleFunc("POST /api/optimizations", c.requireOrigin(c.requireAuth(c.handleOptimize)))
	c.mux.HandleFunc("GET /api/optimizations/{requestId}", c.requireAuth(c.handleRunGet))

	if c.assets != nil {
		c.mux.Handle("GET /", http.FileServerFS(c.assets))
	}
}

func (c *Client) ServeHTTP(w http.ResponseWriter, r *http.Request) { c.mux.ServeHTTP(w, r) }

// ---- responses ----

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// API responses carry session-scoped user data; they must not be reused from a
	// shared browser cache after logout.
	w.Header().Set("Cache-Control", "no-store")
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":{"code":"internal","message":"response encoding failed"}}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	w.Write(body)
}

func fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: message}})
}

// internal reports an unexpected failure. The underlying error goes to the log
// only: it may quote request text or upstream bodies.
func (c *Client) internal(w http.ResponseWriter, context string, err error) {
	c.log.Printf("error: %s: %v", context, err)
	fail(w, http.StatusInternalServerError, "internal", "internal error")
}

// decodeJSON reads the body into v, bounded by MaxBodyBytes. It rejects an empty
// body and trailing content so a truncated request cannot be half-applied.
func (c *Client) decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, c.cfg.maxBodyBytes())
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		c.reportBodyError(w, err)
		return false
	}
	// A second value in the body means the request is not the single JSON object
	// this API expects; half-applying it would be worse than rejecting it.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			c.reportBodyError(w, err)
			return false
		}
		fail(w, http.StatusBadRequest, "bad_request", "request body must contain exactly one JSON value")
		return false
	}
	return true
}

// reportBodyError distinguishes an oversized body from a malformed one; an
// oversized body read past the limit surfaces as a read error, not a JSON error.
func (c *Client) reportBodyError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		fail(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
		return
	}
	if errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "bad_request", "request body is empty")
		return
	}
	fail(w, http.StatusBadRequest, "bad_request", "request body is not valid JSON")
}

// ---- middleware ----

// requireOrigin enforces same-origin on state-changing requests. A request
// without an Origin header (curl, the evaluation script) is allowed: only a
// browser attaches one, and a browser is the only client that can be made to
// issue a cross-site request with the user's cookie.
func (c *Client) requireOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !c.originAllowed(origin) {
			fail(w, http.StatusForbidden, "bad_origin", "origin not allowed")
			return
		}
		next(w, r)
	}
}

func (c *Client) originAllowed(origin string) bool {
	for _, allowed := range c.cfg.AllowedOrigins {
		if subtle.ConstantTimeCompare([]byte(origin), []byte(allowed)) == 1 {
			return true
		}
	}
	return false
}

func (c *Client) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := c.session.lookup(c.cookieToken(r)); !ok {
			fail(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		next(w, r)
	}
}

// ---- session endpoints ----

func (c *Client) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	_, ok := c.session.lookup(c.cookieToken(r))
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": ok})
}

type loginRequest struct {
	Password string `json:"password"`
}

func (c *Client) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if retryAfter, blocked := c.limiter.blocked(ip, time.Now()); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		fail(w, http.StatusTooManyRequests, "rate_limited", "too many failed login attempts")
		return
	}
	var req loginRequest
	if !c.decodeJSON(w, r, &req) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Password), []byte(c.cfg.Password)) != 1 {
		c.limiter.fail(ip, time.Now())
		fail(w, http.StatusUnauthorized, "unauthorized", "invalid password")
		return
	}
	c.limiter.succeed(ip)
	token, err := c.session.create()
	if err != nil {
		c.internal(w, "create session", err)
		return
	}
	c.setSessionCookie(w, token, int(c.cfg.SessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (c *Client) handleLogout(w http.ResponseWriter, r *http.Request) {
	c.session.drop(c.cookieToken(r))
	c.setSessionCookie(w, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (c *Client) setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (c *Client) cookieToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- helpers for handlers ----

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, "bad_request", "id must be a positive integer")
		return 0, false
	}
	return id, true
}

func notFoundOrInternal(c *Client, w http.ResponseWriter, context string, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	c.internal(w, context, err)
}

// trimmed returns the value with surrounding whitespace removed, and reports
// whether anything was left.
func trimmed(s string) (string, bool) {
	t := strings.TrimSpace(s)
	return t, t != ""
}

// randomToken returns 32 bytes of entropy, hex encoded.
func randomToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
