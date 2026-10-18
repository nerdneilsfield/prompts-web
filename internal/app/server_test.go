package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

const (
	testPassword   = "correct horse battery staple"
	testAPIKey     = "sk-test-key-do-not-log"
	testOrigin     = "https://prompts.example.com"
	testSchemeName = "默认优化"
	testSchemeBody = "保留原意，占位缺失事实。"
)

// mockUpstream is a stand-in for the OpenAI-compatible endpoint. It counts calls
// so a test can assert that a duplicate request ID produced exactly one, and
// records the last request so a test can inspect what was actually sent.
type mockUpstream struct {
	*httptest.Server
	calls   atomic.Int64
	handler atomic.Value // func(w http.ResponseWriter, r *http.Request)

	mu       sync.Mutex
	lastBody string
	lastAuth string
}

func newMockUpstream(t *testing.T) *mockUpstream {
	t.Helper()
	m := &mockUpstream{}
	m.handler.Store(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"优化结果"}}]}`)
	})
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		m.mu.Lock()
		m.lastBody = string(body)
		m.lastAuth = r.Header.Get("Authorization")
		m.mu.Unlock()
		m.handler.Load().(func(http.ResponseWriter, *http.Request))(w, r)
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *mockUpstream) reply(fn func(w http.ResponseWriter, r *http.Request)) {
	m.handler.Store(fn)
}

func (m *mockUpstream) replyJSON(status int, body string) {
	m.reply(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

// lastRequest returns the decoded upstream request body as text, for assertions
// about what the model was actually sent.
func (m *mockUpstream) lastRequest(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastBody == "" {
		t.Fatal("no request was recorded by the mock upstream")
	}
	var decoded struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal([]byte(m.lastBody), &decoded); err != nil {
		t.Fatalf("upstream body is not JSON: %v (%s)", err, m.lastBody)
	}
	var sb strings.Builder
	for _, msg := range decoded.Messages {
		sb.WriteString(msg.Role + ":" + msg.Content + "\n")
	}
	sb.WriteString("auth:" + m.lastAuth)
	if decoded.Stream {
		sb.WriteString("\nstream:true")
	}
	return sb.String()
}

type testEnv struct {
	client   *Client
	server   *httptest.Server
	store    *Store
	upstream *mockUpstream
	dbPath   string
	cfg      Config
	logs     *syncBuffer
}

// syncBuffer collects log output for assertions across concurrent handlers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// itoa formats an integer primary key for a URL or JSON literal.
func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func newTestEnv(t *testing.T, mutate func(*Config)) *testEnv {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	upstream := newMockUpstream(t)

	cfg := DefaultConfig()
	cfg.Password = testPassword
	cfg.DBPath = dbPath
	cfg.AllowedOrigins = []string{testOrigin}
	cfg.BaseURL = upstream.URL + "/v1"
	cfg.APIKey = testAPIKey
	cfg.Model = "test-model"
	cfg.RequestTimeout = 2 * time.Second
	if mutate != nil {
		mutate(&cfg)
	}

	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	logs := &syncBuffer{}
	client, err := newServer(store, cfg, nil, log.New(logs, "", 0))
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	server := httptest.NewServer(client)
	t.Cleanup(func() {
		server.Close()
		store.Close()
	})
	return &testEnv{client: client, server: server, store: store, upstream: upstream, dbPath: dbPath, cfg: cfg, logs: logs}
}

// reopen rebuilds the client over the same database file, which is how a process
// restart is modelled. The session table and the served handler are replaced,
// just as a new process would.
func (e *testEnv) reopen(t *testing.T) {
	t.Helper()
	e.store.Close()
	store, err := OpenStore(e.dbPath)
	if err != nil {
		t.Fatalf("reopen OpenStore: %v", err)
	}
	client, err := newServer(store, e.cfg, nil, log.New(e.logs, "", 0))
	if err != nil {
		t.Fatalf("reopen newServer: %v", err)
	}
	e.server.Close()
	e.store, e.client = store, client
	e.server = httptest.NewServer(client)
}

func (e *testEnv) request(t *testing.T, method, path string, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, raw
}

// login authenticates and returns the cookie to send on later requests.
func (e *testEnv) login(t *testing.T) *http.Cookie {
	t.Helper()
	resp, _ := e.request(t, http.MethodPost, "/api/session", `{"password":"`+testPassword+`"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("login set no session cookie")
	return nil
}

func cookieHeader(c *http.Cookie) map[string]string {
	return map[string]string{"Cookie": c.Name + "=" + c.Value}
}

func withCookie(base map[string]string, c *http.Cookie) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	out["Cookie"] = c.Name + "=" + c.Value
	return out
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return v
}

func errorCode(t *testing.T, raw []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", raw, err)
	}
	if payload.Error.Code == "" {
		t.Fatalf("error body %q has no error.code", raw)
	}
	return payload.Error.Code
}

// ---- session, auth, origin ----

func TestSessionGetIsAnonymous(t *testing.T) {
	env := newTestEnv(t, nil)
	resp, raw := env.request(t, http.MethodGet, "/api/session", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := decode[map[string]bool](t, raw)["authenticated"]; got {
		t.Fatalf("authenticated = true before login")
	}

	cookie := env.login(t)
	resp, raw = env.request(t, http.MethodGet, "/api/session", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK || !decode[map[string]bool](t, raw)["authenticated"] {
		t.Fatalf("after login: status %d body %s", resp.StatusCode, raw)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	env := newTestEnv(t, nil)
	resp, raw := env.request(t, http.MethodPost, "/api/session", `{"password":"wrong"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if code := errorCode(t, raw); code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", code)
	}
	if got := len(resp.Cookies()); got != 0 {
		t.Fatalf("failed login set %d cookies, want 0", got)
	}
}

func TestLoginLimiterBlocksAfterRepeatedFailures(t *testing.T) {
	env := newTestEnv(t, func(c *Config) {
		c.LoginMaxFailures = 3
		c.LoginWindow = time.Minute
	})
	for i := 0; i < 3; i++ {
		resp, _ := env.request(t, http.MethodPost, "/api/session", `{"password":"wrong"}`, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i, resp.StatusCode)
		}
	}
	resp, raw := env.request(t, http.MethodPost, "/api/session", `{"password":"wrong"}`, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if code := errorCode(t, raw); code != "rate_limited" {
		t.Fatalf("code = %q, want rate_limited", code)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("429 has no Retry-After header")
	}
	// The throttle must not hand out a session to a correct password either.
	resp, _ = env.request(t, http.MethodPost, "/api/session", `{"password":"`+testPassword+`"}`, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("correct password while blocked: status = %d, want 429", resp.StatusCode)
	}
}

// TestLoginLimiterHoldsUnderConcurrentAttempts is the regression for the original
// race: the quota was checked before the body was read and the counter was only
// updated after a wrong password, so simultaneous guesses all passed the check
// and all reached the comparison. Every admitted attempt must now be counted.
func TestLoginLimiterHoldsUnderConcurrentAttempts(t *testing.T) {
	for _, max := range []int{1, 3} {
		t.Run(strconv.Itoa(max), func(t *testing.T) {
			env := newTestEnv(t, func(c *Config) {
				c.LoginMaxFailures = max
				c.LoginWindow = time.Minute
			})

			const attempts = 16
			var reachedComparison atomic.Int64
			// Count the attempts that actually get past the throttle by observing the
			// password comparison indirectly: only an admitted attempt returns 401 (a
			// rejected one returns 429), so the two status codes partition the racers.
			type outcome struct{ status int }
			results := make([]outcome, attempts)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < attempts; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					req, err := http.NewRequest(http.MethodPost, env.server.URL+"/api/session", strings.NewReader(`{"password":"wrong"}`))
					if err != nil {
						t.Errorf("build request: %v", err)
						return
					}
					req.Header.Set("Content-Type", "application/json")
					<-start
					resp, err := env.server.Client().Do(req)
					if err != nil {
						t.Errorf("attempt %d: %v", i, err)
						return
					}
					defer resp.Body.Close()
					io.Copy(io.Discard, resp.Body)
					results[i].status = resp.StatusCode
					if resp.StatusCode == http.StatusUnauthorized {
						reachedComparison.Add(1)
					}
				}(i)
			}
			close(start)
			wg.Wait()

			for i, r := range results {
				if r.status != http.StatusUnauthorized && r.status != http.StatusTooManyRequests {
					t.Fatalf("attempt %d status = %d, want 401 or 429", i, r.status)
				}
			}
			if got := reachedComparison.Load(); got > int64(max) {
				t.Fatalf("%d attempts reached the password comparison, limit is %d", got, max)
			}
			if got := reachedComparison.Load(); got != int64(max) {
				t.Fatalf("admitted attempts = %d, want exactly %d (no quota left unused)", got, max)
			}
			// After the storm the throttle is still closed, and a correct password
			// does not open it.
			resp, _ := env.request(t, http.MethodPost, "/api/session", `{"password":"`+testPassword+`"}`, nil)
			if resp.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("correct password after the storm: status = %d, want 429", resp.StatusCode)
			}
		})
	}
}

// TestLoginLimiterAdmitsAgainAfterWindow checks the reservation is released on
// success and expires with the window, so the throttle is not a permanent lock.
func TestLoginLimiterAdmitsAgainAfterWindow(t *testing.T) {
	env := newTestEnv(t, func(c *Config) {
		c.LoginMaxFailures = 1
		c.LoginWindow = 100 * time.Millisecond
	})
	resp, _ := env.request(t, http.MethodPost, "/api/session", `{"password":"wrong"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("first attempt status = %d, want 401", resp.StatusCode)
	}
	resp, _ = env.request(t, http.MethodPost, "/api/session", `{"password":"wrong"}`, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second attempt status = %d, want 429", resp.StatusCode)
	}
	time.Sleep(150 * time.Millisecond)
	resp, raw := env.request(t, http.MethodPost, "/api/session", `{"password":"`+testPassword+`"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("after the window: status = %d, body %s", resp.StatusCode, raw)
	}
}

func TestLoginLimiterReleasesQuotaOnSuccess(t *testing.T) {
	env := newTestEnv(t, func(c *Config) {
		c.LoginMaxFailures = 2
		c.LoginWindow = time.Minute
	})
	resp, _ := env.request(t, http.MethodPost, "/api/session", `{"password":"wrong"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	resp, _ = env.request(t, http.MethodPost, "/api/session", `{"password":"`+testPassword+`"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	// The successful login cleared the record, so the full quota is available
	// again rather than one attempt having been consumed.
	for i := 0; i < 2; i++ {
		resp, _ = env.request(t, http.MethodPost, "/api/session", `{"password":"wrong"}`, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d after success: status = %d, want 401", i, resp.StatusCode)
		}
	}
	resp, _ = env.request(t, http.MethodPost, "/api/session", `{"password":"wrong"}`, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("fourth attempt: status = %d, want 429", resp.StatusCode)
	}
}

func TestWritesRequireAuthentication(t *testing.T) {
	env := newTestEnv(t, nil)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/prompts", ""},
		{http.MethodPost, "/api/prompts", `{"title":"t","body":"b"}`},
		{http.MethodGet, "/api/schemes", ""},
		{http.MethodPost, "/api/schemes", `{"name":"n","body":"b"}`},
		{http.MethodPost, "/api/optimizations", `{"request_id":"r1","input":"i","scheme_id":1}`},
		{http.MethodGet, "/api/optimizations/r1", ""},
		{http.MethodDelete, "/api/session", ""},
	} {
		resp, raw := env.request(t, tc.method, tc.path, tc.body, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
		if code := errorCode(t, raw); code != "unauthorized" {
			t.Fatalf("%s %s code = %q", tc.method, tc.path, code)
		}
	}
}

func TestOriginCheckedOnWrites(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	body := `{"title":"标题","body":"正文"}`

	resp, raw := env.request(t, http.MethodPost, "/api/prompts", body, withCookie(map[string]string{"Origin": "https://evil.example.com"}, cookie))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403 (body %s)", resp.StatusCode, raw)
	}
	if code := errorCode(t, raw); code != "bad_origin" {
		t.Fatalf("code = %q, want bad_origin", code)
	}

	// A look-alike origin must not pass a strict comparison.
	resp, _ = env.request(t, http.MethodPost, "/api/prompts", body, withCookie(map[string]string{"Origin": "https://evil-prompts.example.com"}, cookie))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("look-alike origin status = %d, want 403", resp.StatusCode)
	}

	resp, raw = env.request(t, http.MethodPost, "/api/prompts", body, withCookie(map[string]string{"Origin": testOrigin}, cookie))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("allowed origin status = %d, want 201 (body %s)", resp.StatusCode, raw)
	}

	// A CLI sends no Origin and must still work.
	resp, raw = env.request(t, http.MethodPost, "/api/prompts", body, cookieHeader(cookie))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("originless status = %d, want 201 (body %s)", resp.StatusCode, raw)
	}
}

func TestSessionSurvivesNothingAcrossRestart(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	env.reopen(t)
	resp, _ := env.request(t, http.MethodGet, "/api/prompts", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status after restart = %d, want 401", resp.StatusCode)
	}
}

// ---- prompts and schemes ----

func TestPromptCRUDSearchAndPersistence(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)

	resp, raw := env.request(t, http.MethodPost, "/api/prompts",
		`{"title":"总结文章","body":"请总结下面这篇文章","tags":[" 总结 ","总结","写作"]}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", resp.StatusCode, raw)
	}
	created := decode[Prompt](t, raw)
	if created.ID == 0 || created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("created prompt missing fields: %+v", created)
	}
	want := []string{"总结", "写作"}
	if len(created.Tags) != len(want) || created.Tags[0] != want[0] || created.Tags[1] != want[1] {
		t.Fatalf("tags = %v, want %v (trimmed, deduplicated, order kept)", created.Tags, want)
	}

	// A second prompt, so ordering and filtering have something to discriminate.
	resp, raw = env.request(t, http.MethodPost, "/api/prompts", `{"title":"翻译","body":"翻译成英文","tags":["翻译"]}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("second create status = %d, body %s", resp.StatusCode, raw)
	}
	second := decode[Prompt](t, raw)

	resp, raw = env.request(t, http.MethodGet, "/api/prompts", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	// The list is a bare JSON array, not an envelope.
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		t.Fatalf("list body is not a JSON array: %s", raw)
	}
	list := decode[[]Prompt](t, raw)
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if list[0].ID != second.ID {
		t.Fatalf("list is not newest-updated-first: got id %d first, want %d", list[0].ID, second.ID)
	}

	// Search is a case-insensitive substring of title or body, server side.
	resp, raw = env.request(t, http.MethodGet, "/api/prompts?q=%E6%80%BB%E7%BB%93", "", cookieHeader(cookie))
	if got := decode[[]Prompt](t, raw); len(got) != 1 || got[0].ID != created.ID {
		t.Fatalf("body search returned %+v, want only prompt %d", got, created.ID)
	}
	resp, raw = env.request(t, http.MethodGet, "/api/prompts?q=TRANSLATE", "", cookieHeader(cookie))
	if got := decode[[]Prompt](t, raw); len(got) != 0 {
		t.Fatalf("unmatched search returned %+v, want empty", got)
	}
	resp, raw = env.request(t, http.MethodGet, "/api/prompts?tag=%E7%BF%BB%E8%AF%91", "", cookieHeader(cookie))
	if got := decode[[]Prompt](t, raw); len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("tag filter returned %+v, want only prompt %d", got, second.ID)
	}

	resp, raw = env.request(t, http.MethodPut, "/api/prompts/"+itoa(created.ID), `{"title":"总结文章 v2","body":"新正文","tags":["总结"]}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d, body %s", resp.StatusCode, raw)
	}
	updated := decode[Prompt](t, raw)
	if updated.Title != "总结文章 v2" || updated.Body != "新正文" {
		t.Fatalf("update did not apply: %+v", updated)
	}
	if updated.CreatedAt != created.CreatedAt {
		t.Fatalf("created_at changed on update: %q -> %q", created.CreatedAt, updated.CreatedAt)
	}

	// Everything must still be there after a restart.
	env.reopen(t)
	cookie = env.login(t)
	resp, raw = env.request(t, http.MethodGet, "/api/prompts/"+itoa(created.ID), "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get after restart status = %d, body %s", resp.StatusCode, raw)
	}
	reloaded := decode[Prompt](t, raw)
	if reloaded.Title != "总结文章 v2" || reloaded.Body != "新正文" || len(reloaded.Tags) != 1 {
		t.Fatalf("prompt after restart = %+v", reloaded)
	}

	resp, _ = env.request(t, http.MethodDelete, "/api/prompts/"+itoa(created.ID), "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp.StatusCode)
	}
	resp, raw = env.request(t, http.MethodGet, "/api/prompts/"+itoa(created.ID), "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted status = %d, want 404", resp.StatusCode)
	}
	if code := errorCode(t, raw); code != "not_found" {
		t.Fatalf("code = %q, want not_found", code)
	}
}

func TestPromptValidation(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	for name, body := range map[string]string{
		"empty title": `{"title":"   ","body":"b"}`,
		"empty body":  `{"title":"t","body":"  "}`,
		"no fields":   `{}`,
	} {
		resp, raw := env.request(t, http.MethodPost, "/api/prompts", body, cookieHeader(cookie))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", name, resp.StatusCode, raw)
		}
	}
	resp, raw := env.request(t, http.MethodPost, "/api/prompts", ``, cookieHeader(cookie))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty body: status = %d, want 400", resp.StatusCode)
	}
	if code := errorCode(t, raw); code != "bad_request" {
		t.Fatalf("empty body code = %q, want bad_request", code)
	}
	resp, _ = env.request(t, http.MethodPost, "/api/prompts", `{"title":"t","body":"b"} trailing`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("trailing content: status = %d, want 400", resp.StatusCode)
	}
	resp, _ = env.request(t, http.MethodPost, "/api/prompts", `{"title":"t","body":"b"}{"title":"u","body":"c"}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("two JSON values: status = %d, want 400", resp.StatusCode)
	}
	resp, _ = env.request(t, http.MethodPost, "/api/prompts", strings.Repeat("x", 4096), cookieHeader(cookie))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed JSON: status = %d, want 400", resp.StatusCode)
	}
	resp, _ = env.request(t, http.MethodGet, "/api/prompts/abc", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-numeric id: status = %d, want 400", resp.StatusCode)
	}
	resp, raw = env.request(t, http.MethodGet, "/api/prompts/9999", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing id: status = %d, want 404 (body %s)", resp.StatusCode, raw)
	}
}

func TestSchemeCRUD(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)

	resp, raw := env.request(t, http.MethodPost, "/api/schemes", `{"name":"精简","body":"去掉冗余"},`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("trailing comma status = %d, want 400", resp.StatusCode)
	}

	resp, raw = env.request(t, http.MethodPost, "/api/schemes", `{"name":" 精简 ","body":"去掉冗余"}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", resp.StatusCode, raw)
	}
	scheme := decode[Scheme](t, raw)
	if scheme.Name != "精简" {
		t.Fatalf("name = %q, want trimmed", scheme.Name)
	}

	resp, raw = env.request(t, http.MethodGet, "/api/schemes", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		t.Fatalf("list status = %d body %s", resp.StatusCode, raw)
	}
	if got := decode[[]Scheme](t, raw); len(got) != 1 || got[0].ID != scheme.ID {
		t.Fatalf("list = %+v, want the created scheme", got)
	}

	resp, raw = env.request(t, http.MethodPut, "/api/schemes/"+itoa(scheme.ID), `{"name":"精简 v2","body":"更短"}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK || decode[Scheme](t, raw).Name != "精简 v2" {
		t.Fatalf("update status = %d body %s", resp.StatusCode, raw)
	}

	env.reopen(t)
	cookie = env.login(t)
	resp, raw = env.request(t, http.MethodGet, "/api/schemes/"+itoa(scheme.ID), "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK || decode[Scheme](t, raw).Name != "精简 v2" {
		t.Fatalf("scheme after restart: status %d body %s", resp.StatusCode, raw)
	}

	resp, _ = env.request(t, http.MethodDelete, "/api/schemes/"+itoa(scheme.ID), "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp.StatusCode)
	}
	resp, _ = env.request(t, http.MethodDelete, "/api/schemes/"+itoa(scheme.ID), "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", resp.StatusCode)
	}
}

// ---- optimizations ----

func createScheme(t *testing.T, env *testEnv, cookie *http.Cookie, name, body string) Scheme {
	t.Helper()
	resp, raw := env.request(t, http.MethodPost, "/api/schemes",
		`{"name":`+mustJSON(t, name)+`,"body":`+mustJSON(t, body)+`}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create scheme status = %d, body %s", resp.StatusCode, raw)
	}
	return decode[Scheme](t, raw)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestOptimizeSuccessRecordsRun(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, testSchemeName, testSchemeBody)

	resp, raw := env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-1","input":"原文内容","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	run := decode[Run](t, raw)
	if run.Status != RunSucceeded || run.Result != "优化结果" {
		t.Fatalf("run = %+v", run)
	}
	if run.Input != "原文内容" || run.SchemeID != scheme.ID {
		t.Fatalf("run did not record input/scheme_id: %+v", run)
	}
	if run.SchemeSnapshot != testSchemeBody || run.SchemeName != testSchemeName {
		t.Fatalf("run did not snapshot the scheme: %+v", run)
	}
	if run.Model != "test-model" || run.FinishedAt == nil || run.CreatedAt == "" {
		t.Fatalf("run metadata incomplete: %+v", run)
	}
	if string(run.Params) == "" || string(run.Params) == "{}" {
		t.Fatalf("run recorded no params: %s", run.Params)
	}

	// The system instruction must reach the model: it is the whole guarantee
	// against invented facts.
	sent := env.upstream.lastRequest(t)
	if !strings.Contains(sent, PreserveInstruction) {
		t.Fatalf("upstream request is missing the preserve instruction: %s", sent)
	}
	if !strings.Contains(sent, testSchemeBody) || !strings.Contains(sent, "原文内容") {
		t.Fatalf("upstream request is missing scheme or input: %s", sent)
	}
	if !strings.Contains(sent, "Bearer "+testAPIKey) {
		t.Fatal("upstream request is missing the bearer key")
	}

	// The run is readable by ID, which is how a lost response is recovered.
	resp, raw = env.request(t, http.MethodGet, "/api/optimizations/req-1", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK || decode[Run](t, raw).Result != "优化结果" {
		t.Fatalf("get run status = %d body %s", resp.StatusCode, raw)
	}
}

func TestRunSnapshotSurvivesSchemeEditAndDelete(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, testSchemeName, testSchemeBody)

	resp, raw := env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-snap","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("optimize status = %d, body %s", resp.StatusCode, raw)
	}
	before := env.upstream.calls.Load()

	// Editing the scheme must not rewrite the recorded snapshot.
	env.request(t, http.MethodPut, "/api/schemes/"+itoa(scheme.ID), `{"name":"改名","body":"完全不同的方案"}`, cookieHeader(cookie))
	resp, raw = env.request(t, http.MethodGet, "/api/optimizations/req-snap", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	run := decode[Run](t, raw)
	if run.SchemeSnapshot != testSchemeBody || run.SchemeName != testSchemeName {
		t.Fatalf("snapshot changed after scheme edit: %+v", run)
	}

	// Deleting the scheme must not lose the run.
	resp, _ = env.request(t, http.MethodDelete, "/api/schemes/"+itoa(scheme.ID), "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete scheme status = %d", resp.StatusCode)
	}
	resp, raw = env.request(t, http.MethodGet, "/api/optimizations/req-snap", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK || decode[Run](t, raw).SchemeSnapshot != testSchemeBody {
		t.Fatalf("run lost after scheme delete: status %d body %s", resp.StatusCode, raw)
	}

	// And replaying the same request still returns the stored run without a
	// second upstream call: the scheme row is not consulted.
	resp, raw = env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-snap","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, body %s", resp.StatusCode, raw)
	}
	if got := decode[Run](t, raw); got.Result != "优化结果" || got.SchemeSnapshot != testSchemeBody {
		t.Fatalf("replay returned %+v", got)
	}
	if env.upstream.calls.Load() != before {
		t.Fatalf("replay called upstream %d extra times", env.upstream.calls.Load()-before)
	}
}

func TestReplayConflictOnDifferentInputOrScheme(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, "方案", "正文")
	other := createScheme(t, env, cookie, "另一个方案", "另一个正文")

	env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-conflict","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
	before := env.upstream.calls.Load()

	for name, body := range map[string]string{
		"different input":  `{"request_id":"req-conflict","input":"不同原文","scheme_id":` + itoa(scheme.ID) + `}`,
		"different scheme": `{"request_id":"req-conflict","input":"原文","scheme_id":` + itoa(other.ID) + `}`,
	} {
		resp, raw := env.request(t, http.MethodPost, "/api/optimizations", body, cookieHeader(cookie))
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s: status = %d, want 409 (body %s)", name, resp.StatusCode, raw)
		}
		if code := errorCode(t, raw); code != "request_id_conflict" {
			t.Fatalf("%s: code = %q, want request_id_conflict", name, code)
		}
	}
	if env.upstream.calls.Load() != before {
		t.Fatal("a conflicted request reached upstream")
	}
}

func TestConcurrentSameRequestIDCallsUpstreamOnce(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, "方案", "正文")

	release := make(chan struct{})
	env.upstream.reply(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the winner inside the model call
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"唯一结果"}}]}`)
	})

	const racers = 8
	type outcome struct {
		status int
		body   []byte
		err    error
	}
	results := make([]outcome, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := `{"request_id":"req-race","input":"原文","scheme_id":` + itoa(scheme.ID) + `}`
			req, err := http.NewRequest(http.MethodPost, env.server.URL+"/api/optimizations", strings.NewReader(payload))
			if err != nil {
				results[i].err = err
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", cookie.Name+"="+cookie.Value)
			<-start
			resp, err := env.server.Client().Do(req)
			if err != nil {
				results[i].err = err
				return
			}
			defer resp.Body.Close()
			results[i].body, _ = io.ReadAll(resp.Body)
			results[i].status = resp.StatusCode
		}(i)
	}
	close(start)
	// Let the losers reach the claim before the winner finishes.
	time.Sleep(150 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := env.upstream.calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want exactly 1", got)
	}
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("racer %d: %v", i, r.err)
		}
		if r.status != http.StatusOK && r.status != http.StatusAccepted {
			t.Fatalf("racer %d status = %d, body %s", i, r.status, r.body)
		}
		run := decode[Run](t, r.body)
		if run.RequestID != "req-race" || run.SchemeID != scheme.ID {
			t.Fatalf("racer %d got run %+v", i, run)
		}
		if r.status == http.StatusOK && run.Status != RunSucceeded {
			t.Fatalf("racer %d got 200 with status %q", i, run.Status)
		}
	}
}

func TestModelUnconfiguredDegradesGracefully(t *testing.T) {
	env := newTestEnv(t, func(c *Config) {
		c.BaseURL = ""
		c.APIKey = ""
		c.Model = ""
	})

	// The server must have started at all; NewServer is called by newTestEnv.
	resp, raw := env.request(t, http.MethodGet, "/api/session", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d, body %s", resp.StatusCode, raw)
	}
	session := decode[map[string]any](t, raw)
	if session["authenticated"] != false {
		t.Fatalf("authenticated = %v, want false", session["authenticated"])
	}
	if session["model_configured"] != false {
		t.Fatalf("model_configured = %v, want false", session["model_configured"])
	}

	// A partial set is still unconfigured; all three fields are required.
	partial := newTestEnv(t, func(c *Config) {
		c.BaseURL = "https://api.example.com/v1"
		c.APIKey = ""
		c.Model = "m"
	})
	resp, raw = partial.request(t, http.MethodGet, "/api/session", "", nil)
	if got := decode[map[string]any](t, raw)["model_configured"]; got != false {
		t.Fatalf("partial config model_configured = %v, want false (status %d)", got, resp.StatusCode)
	}

	// Login, prompts and schemes all keep working without a model.
	cookie := env.login(t)
	resp, raw = env.request(t, http.MethodGet, "/api/session", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK || decode[map[string]any](t, raw)["authenticated"] != true {
		t.Fatalf("authenticated session: status %d body %s", resp.StatusCode, raw)
	}
	if got := decode[map[string]any](t, raw)["model_configured"]; got != false {
		t.Fatalf("authenticated model_configured = %v, want false", got)
	}

	resp, raw = env.request(t, http.MethodPost, "/api/prompts", `{"title":"标题","body":"正文"}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create prompt status = %d, body %s", resp.StatusCode, raw)
	}
	scheme := createScheme(t, env, cookie, "方案", "正文")

	// A new optimization is refused as unconfigured, with no upstream call and no
	// running row left behind.
	resp, raw = env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-nomodel","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("optimize status = %d, want 503 (body %s)", resp.StatusCode, raw)
	}
	if code := errorCode(t, raw); code != "model_not_configured" {
		t.Fatalf("code = %q, want model_not_configured", code)
	}
	if env.upstream.calls.Load() != 0 {
		t.Fatalf("unconfigured optimize reached upstream %d times", env.upstream.calls.Load())
	}
	if _, err := env.store.Run("req-nomodel"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unconfigured optimize left a run row: %v", err)
	}

	// A run recorded while the model was configured stays queryable and replayable
	// once the endpoint is unconfigured, because replay never consults the model.
	// That is the state a restart with a lost key produces.
	if _, err := env.store.ClaimRun(Run{
		RequestID: "req-existing", Input: "原文", SchemeID: scheme.ID,
		SchemeName: scheme.Name, SchemeSnapshot: scheme.Body, Model: "old-model",
	}); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := env.store.FinishRun("req-existing", RunSucceeded, "旧结果", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	resp, raw = env.request(t, http.MethodGet, "/api/optimizations/req-existing", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK || decode[Run](t, raw).Result != "旧结果" {
		t.Fatalf("existing run unreadable while unconfigured: status %d body %s", resp.StatusCode, raw)
	}
	resp, raw = env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-existing","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK || decode[Run](t, raw).Result != "旧结果" {
		t.Fatalf("replay while unconfigured: status %d body %s", resp.StatusCode, raw)
	}
	if env.upstream.calls.Load() != 0 {
		t.Fatal("replay reached upstream")
	}
}

func TestLoginResponseReportsModelConfigured(t *testing.T) {
	for name, configured := range map[string]bool{"model configured": true, "model unconfigured": false} {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t, func(c *Config) {
				if !configured {
					c.BaseURL, c.APIKey, c.Model = "", "", ""
				}
			})

			resp, raw := env.request(t, http.MethodPost, "/api/session", `{"password":"`+testPassword+`"}`, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("login status = %d, body %s", resp.StatusCode, raw)
			}
			session := decode[map[string]any](t, raw)
			if session["authenticated"] != true {
				t.Fatalf("authenticated = %v, want true", session["authenticated"])
			}
			// The login response carries the same derived flag as GET, so the frontend
			// can decide whether to offer the optimize entry without a second call.
			if session["model_configured"] != configured {
				t.Fatalf("model_configured = %v, want %v (body %s)", session["model_configured"], configured, raw)
			}
		})
	}
}

func TestModelConfiguredIsReported(t *testing.T) {
	env := newTestEnv(t, nil)
	resp, raw := env.request(t, http.MethodGet, "/api/session", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := decode[map[string]any](t, raw)["model_configured"]; got != true {
		t.Fatalf("model_configured = %v, want true", got)
	}
}

// TestTerminalWriteFailureIsNotReportedAsSuccess covers the run that is claimed,
// executed and then cannot be settled. The response must report the storage
// failure instead of a 200 carrying a still-running row, and the model must not
// be called again.
//
// The failure is injected through SQLite itself: the connection is put into
// query_only mode while the model call is in flight, so writes fail and reads
// still work. This is the case that matters, because reads working is what lets
// the old code read back the unfinished row and return it as a 200. The test
// exercises the real FinishRun path with no test hook.
func TestTerminalWriteFailureIsNotReportedAsSuccess(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, "方案", "正文")

	entered := make(chan struct{}, 1)
	written := make(chan struct{})
	env.upstream.reply(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		// Hold the response until query_only is confirmed on, so the terminal write
		// is guaranteed to fail. Without this barrier the test leans on the goroutine
		// winning a race it has no synchronization for.
		<-written
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"结果"}}]}`)
	})

	go func() {
		<-entered
		if _, err := env.store.db.Exec(`PRAGMA query_only=ON`); err != nil {
			t.Errorf("enable query_only: %v", err)
		}
		close(written)
	}()

	resp, raw := env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-broken","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", resp.StatusCode, raw)
	}
	if code := errorCode(t, raw); code != "internal" {
		t.Fatalf("code = %q, want internal", code)
	}
	// The response must not claim a terminal state it failed to store: a 200 with
	// status "running" is exactly the false success this guards against.
	if bytes.Contains(raw, []byte(RunRunning)) || bytes.Contains(raw, []byte("结果")) {
		t.Fatalf("500 response presented run state: %s", raw)
	}
	if got := env.upstream.calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (no retry after a storage failure)", got)
	}

	// The row is still readable and still running, so a later GET can report the
	// truth rather than a lost result.
	if _, err := env.store.db.Exec(`PRAGMA query_only=OFF`); err != nil {
		t.Fatalf("disable query_only: %v", err)
	}
	run, err := env.store.Run("req-broken")
	if err != nil {
		t.Fatalf("run should remain readable: %v", err)
	}
	if run.Status != RunRunning {
		t.Fatalf("run status = %q, want still %q", run.Status, RunRunning)
	}
}

func TestUpstreamFailuresAreClassified(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantKind   string
		reachesUp  bool
		extraDelay time.Duration
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":"SECRET_UPSTREAM_DETAIL"}`, wantKind: ErrKindAuth, reachesUp: true},
		{name: "forbidden", status: http.StatusForbidden, body: `SECRET_UPSTREAM_DETAIL`, wantKind: ErrKindAuth, reachesUp: true},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `SECRET_UPSTREAM_DETAIL`, wantKind: ErrKindRateLimit, reachesUp: true},
		{name: "server error", status: http.StatusInternalServerError, body: `SECRET_UPSTREAM_DETAIL`, wantKind: ErrKindUpstream, reachesUp: true},
		{name: "payment required", status: http.StatusPaymentRequired, body: `SECRET_UPSTREAM_DETAIL`, wantKind: ErrKindUpstream, reachesUp: true},
		{name: "not json", status: http.StatusOK, body: `<html>SECRET_UPSTREAM_DETAIL</html>`, wantKind: ErrKindInvalid},
		{name: "no choices", status: http.StatusOK, body: `{"choices":[]}`, wantKind: ErrKindInvalid},
		{name: "empty content", status: http.StatusOK, body: `{"choices":[{"message":{"content":"   "}}]}`, wantKind: ErrKindInvalid},
		{name: "blank body", status: http.StatusOK, body: ``, wantKind: ErrKindInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, nil)
			cookie := env.login(t)
			scheme := createScheme(t, env, cookie, "方案", "正文")
			env.upstream.replyJSON(tc.status, tc.body)

			requestID := "req-fail"
			resp, raw := env.request(t, http.MethodPost, "/api/optimizations",
				`{"request_id":"`+requestID+`","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
			}
			run := decode[Run](t, raw)
			if run.Status != RunFailed {
				t.Fatalf("status = %q, want failed (run %+v)", run.Status, run)
			}
			if run.ErrorKind != tc.wantKind {
				t.Fatalf("error_kind = %q, want %q", run.ErrorKind, tc.wantKind)
			}
			if run.Result != "" {
				t.Fatalf("failed run kept a result: %q", run.Result)
			}
			if run.FinishedAt == nil {
				t.Fatal("failed run has no finished_at")
			}
			// The upstream error text must not reach the client.
			if bytes.Contains(raw, []byte("SECRET_UPSTREAM_DETAIL")) {
				t.Fatalf("upstream error body leaked to client: %s", raw)
			}
			// The failure is durable and still readable.
			resp, raw = env.request(t, http.MethodGet, "/api/optimizations/"+requestID, "", cookieHeader(cookie))
			if resp.StatusCode != http.StatusOK || decode[Run](t, raw).ErrorKind != tc.wantKind {
				t.Fatalf("stored run after failure: status %d body %s", resp.StatusCode, raw)
			}
		})
	}
}

func TestUpstreamTimeout(t *testing.T) {
	env := newTestEnv(t, func(c *Config) { c.RequestTimeout = 150 * time.Millisecond })
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, "方案", "正文")

	released := make(chan struct{})
	env.upstream.reply(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done(): // the proxy must cancel upstream on timeout
		case <-released:
		}
	})

	start := time.Now()
	resp, raw := env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-timeout","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
	elapsed := time.Since(start)
	close(released)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	run := decode[Run](t, raw)
	if run.Status != RunFailed || run.ErrorKind != ErrKindTimeout {
		t.Fatalf("run = %+v, want failed/timeout", run)
	}
	if elapsed > time.Second {
		t.Fatalf("timeout took %v, want it bounded by the configured 150ms", elapsed)
	}
}

func TestClientDisconnectCancelsRun(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, "方案", "正文")

	upstreamEntered := make(chan struct{}, 1)
	upstreamLeft := make(chan struct{})
	env.upstream.reply(func(w http.ResponseWriter, r *http.Request) {
		upstreamEntered <- struct{}{}
		<-r.Context().Done() // upstream sees the cancellation propagate
		close(upstreamLeft)
	})

	ctx, cancel := context.WithCancel(context.Background())
	payload := `{"request_id":"req-cancel","input":"原文","scheme_id":` + itoa(scheme.ID) + `}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.server.URL+"/api/optimizations", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie.Name+"="+cookie.Value)

	done := make(chan error, 1)
	go func() {
		resp, err := env.server.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()

	select {
	case <-upstreamEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream was never called")
	}
	cancel()

	select {
	case <-upstreamLeft:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancel did not propagate to the upstream call")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return after cancellation")
	}

	// The abandoned run must be recorded as canceled, not left running.
	deadline := time.Now().Add(3 * time.Second)
	var run Run
	for {
		run, err = env.store.Run("req-cancel")
		if err == nil && run.Status != RunRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never settled: %+v (err %v)", run, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.Status != RunFailed || run.ErrorKind != ErrKindCanceled {
		t.Fatalf("run = %+v, want failed/canceled", run)
	}
}

func TestRunningRunsAreInterruptedOnRestart(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, "方案", "正文")

	// A run that was claimed but never settled is what a crash leaves behind.
	claimed, err := env.store.ClaimRun(Run{
		RequestID: "req-orphan", Input: "原文", SchemeID: scheme.ID,
		SchemeName: scheme.Name, SchemeSnapshot: scheme.Body, Model: "test-model",
	})
	if err != nil || !claimed {
		t.Fatalf("ClaimRun: claimed=%v err=%v", claimed, err)
	}
	// A settled run must be left alone by the sweep.
	if _, err := env.store.ClaimRun(Run{RequestID: "req-done", Input: "x", SchemeID: scheme.ID, SchemeName: scheme.Name, SchemeSnapshot: scheme.Body, Model: "test-model"}); err != nil {
		t.Fatalf("ClaimRun settled: %v", err)
	}
	if err := env.store.FinishRun("req-done", RunSucceeded, "结果", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	env.reopen(t)
	cookie = env.login(t)

	resp, raw := env.request(t, http.MethodGet, "/api/optimizations/req-orphan", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	orphan := decode[Run](t, raw)
	if orphan.Status != RunInterrupted || orphan.ErrorKind != RunInterrupted {
		t.Fatalf("orphan run = %+v, want interrupted", orphan)
	}
	if orphan.FinishedAt == nil {
		t.Fatal("interrupted run has no finished_at")
	}

	resp, raw = env.request(t, http.MethodGet, "/api/optimizations/req-done", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := decode[Run](t, raw); got.Status != RunSucceeded || got.Result != "结果" {
		t.Fatalf("settled run was altered by the sweep: %+v", got)
	}
}

func TestEmptyListIsArrayNotNull(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	for _, path := range []string{"/api/prompts", "/api/schemes"} {
		resp, raw := env.request(t, http.MethodGet, path, "", cookieHeader(cookie))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, resp.StatusCode)
		}
		if strings.TrimSpace(string(raw)) != "[]" {
			t.Fatalf("%s empty list body = %s, want []", path, raw)
		}
	}
}

func TestOversizedRequestBodyRejected(t *testing.T) {
	env := newTestEnv(t, func(c *Config) { c.MaxBodyBytes = 256 })
	cookie := env.login(t)

	body := `{"title":"t","body":"` + strings.Repeat("x", 1024) + `"}`
	resp, raw := env.request(t, http.MethodPost, "/api/prompts", body, cookieHeader(cookie))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", resp.StatusCode, raw)
	}
	if code := errorCode(t, raw); code != "too_large" {
		t.Fatalf("code = %q, want too_large", code)
	}
	resp, raw = env.request(t, http.MethodGet, "/api/prompts", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	if list := decode[[]Prompt](t, raw); len(list) != 0 {
		t.Fatalf("oversized request created %d prompts", len(list))
	}
}

func TestOversizedModelResponseRejected(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, "方案", "正文")

	// A response larger than the read cap must fail as an invalid response, not
	// be buffered and stored.
	huge := strings.Repeat("a", maxModelResponse+1024)
	env.upstream.replyJSON(http.StatusOK, `{"choices":[{"message":{"content":"`+huge+`"}}]}`)

	resp, raw := env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-huge","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	run := decode[Run](t, raw)
	if run.Status != RunFailed || run.ErrorKind != ErrKindInvalid {
		t.Fatalf("run = %+v, want failed/invalid", run)
	}
	if run.Result != "" {
		t.Fatalf("oversized result was stored (%d bytes)", len(run.Result))
	}
}

func TestOptimizeValidation(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, "方案", "正文")

	for name, body := range map[string]string{
		"no request id":         `{"input":"原文","scheme_id":` + itoa(scheme.ID) + `}`,
		"blank input":           `{"request_id":"r","input":"  ","scheme_id":` + itoa(scheme.ID) + `}`,
		"missing scheme":        `{"request_id":"r","input":"原文","scheme_id":424242}`,
		"no scheme id":          `{"request_id":"r","input":"原文"}`,
		"request id with slash": `{"request_id":"a/b","input":"原文","scheme_id":` + itoa(scheme.ID) + `}`,
	} {
		resp, raw := env.request(t, http.MethodPost, "/api/optimizations", body, cookieHeader(cookie))
		if resp.StatusCode < 400 || resp.StatusCode > 499 {
			t.Fatalf("%s: status = %d, want 4xx (body %s)", name, resp.StatusCode, raw)
		}
		if code := errorCode(t, raw); code == "" {
			t.Fatalf("%s: no error code", name)
		}
	}
	if env.upstream.calls.Load() != 0 {
		t.Fatalf("a rejected request reached upstream %d times", env.upstream.calls.Load())
	}
}

// TestPromptSourceRunMustBeSettled checks that provenance points at a real
// successful run, not at one that failed.
func TestPromptSourceRunMustBeSettled(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, "方案", "正文")
	env.upstream.replyJSON(http.StatusInternalServerError, "boom")

	env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-bad","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))

	resp, raw := env.request(t, http.MethodPost, "/api/prompts",
		`{"title":"t","body":"b","source_run_id":"req-bad"}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("failed source status = %d, want 409 (body %s)", resp.StatusCode, raw)
	}
	resp, raw = env.request(t, http.MethodPost, "/api/prompts",
		`{"title":"t","body":"b","source_run_id":"req-missing"}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("missing source status = %d, want 409 (body %s)", resp.StatusCode, raw)
	}

	env.upstream.replyJSON(http.StatusOK, `{"choices":[{"message":{"content":"好结果"}}]}`)
	env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-good","input":"原文","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
	resp, raw = env.request(t, http.MethodPost, "/api/prompts",
		`{"title":"t","body":"b","source_run_id":"req-good"}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("successful source status = %d, want 201 (body %s)", resp.StatusCode, raw)
	}
	if got := decode[Prompt](t, raw).SourceRunID; got == nil || *got != "req-good" {
		t.Fatalf("source_run_id = %v", got)
	}
}

func TestSecretsAndBodyTextStayOutOfLogs(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	scheme := createScheme(t, env, cookie, "方案名", "方案正文内容")
	env.upstream.replyJSON(http.StatusInternalServerError, `{"error":"SECRET_UPSTREAM_DETAIL"}`)

	env.request(t, http.MethodPost, "/api/optimizations",
		`{"request_id":"req-log","input":"原文机密内容","scheme_id":`+itoa(scheme.ID)+`}`, cookieHeader(cookie))
	env.request(t, http.MethodPost, "/api/session", `{"password":"guess-me"}`, nil)

	logs := env.logs.String()
	for _, secret := range []string{testAPIKey, "原文机密内容", "方案正文内容", "SECRET_UPSTREAM_DETAIL", "guess-me"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("log output contains %q:\n%s", secret, logs)
		}
	}
}

func TestSessionCookieFlags(t *testing.T) {
	for name, secure := range map[string]bool{"production": true, "local http dev": false} {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t, func(c *Config) { c.CookieSecure = secure })
			resp, _ := env.request(t, http.MethodPost, "/api/session", `{"password":"`+testPassword+`"}`, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("login status = %d", resp.StatusCode)
			}
			cookies := resp.Cookies()
			if len(cookies) != 1 {
				t.Fatalf("got %d cookies, want 1", len(cookies))
			}
			cookie := cookies[0]
			if !cookie.HttpOnly {
				t.Fatal("session cookie is not HttpOnly")
			}
			if cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("SameSite = %v, want Strict", cookie.SameSite)
			}
			if cookie.Secure != secure {
				t.Fatalf("Secure = %v, want %v", cookie.Secure, secure)
			}
			if cookie.Value == testPassword {
				t.Fatal("cookie carries the password instead of a random token")
			}
		})
	}
}

func TestSessionExpires(t *testing.T) {
	env := newTestEnv(t, func(c *Config) { c.SessionTTL = 50 * time.Millisecond })
	cookie := env.login(t)
	resp, _ := env.request(t, http.MethodGet, "/api/prompts", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh session status = %d, want 200", resp.StatusCode)
	}
	time.Sleep(80 * time.Millisecond)
	resp, _ = env.request(t, http.MethodGet, "/api/prompts", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session status = %d, want 401", resp.StatusCode)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	resp, _ := env.request(t, http.MethodDelete, "/api/session", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", resp.StatusCode)
	}
	resp, _ = env.request(t, http.MethodGet, "/api/prompts", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status after logout = %d, want 401", resp.StatusCode)
	}
}

func TestUnknownRoutesAndMethods(t *testing.T) {
	env := newTestEnv(t, nil)
	cookie := env.login(t)
	resp, _ := env.request(t, http.MethodGet, "/api/nope", "", cookieHeader(cookie))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404", resp.StatusCode)
	}
	resp, _ = env.request(t, http.MethodPatch, "/api/prompts", `{}`, cookieHeader(cookie))
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("unsupported method status = %d, want 405", resp.StatusCode)
	}
}

func TestAssetsServeWhenProvided(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "assets.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	cfg := DefaultConfig()
	cfg.Password = testPassword
	cfg.DBPath = dbPath
	cfg.BaseURL = "https://api.example.com/v1"
	cfg.APIKey = testAPIKey
	cfg.Model = "test-model"
	assets := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>app</html>")},
	}
	handler, err := NewServer(store, cfg, assets)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/")
	if err != nil {
		t.Fatalf("get /: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "app") {
		t.Fatalf("status = %d body %s", resp.StatusCode, body)
	}
	// The API still answers under a mounted asset tree.
	resp, err = server.Client().Get(server.URL + "/api/session")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d", resp.StatusCode)
	}
}

func TestNewServerRejectsBadConfig(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cfg.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	base := DefaultConfig()
	base.Password = testPassword
	base.DBPath = "x.db"
	base.BaseURL = "https://api.example.com/v1"
	base.APIKey = testAPIKey
	base.Model = "test-model"

	for name, mutate := range map[string]func(*Config){
		"no password":     func(c *Config) { c.Password = "" },
		"no session ttl":  func(c *Config) { c.SessionTTL = 0 },
		"no request ttl":  func(c *Config) { c.RequestTimeout = 0 },
		"bad origin":      func(c *Config) { c.AllowedOrigins = []string{"prompts.example.com"} },
		"no login window": func(c *Config) { c.LoginWindow = 0 },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := NewServer(store, cfg, nil); err == nil {
			t.Fatalf("%s: NewServer accepted an invalid config", name)
		}
	}
	if _, err := NewServer(nil, base, nil); err == nil {
		t.Fatal("NewServer accepted a nil store")
	}

	// A missing or partial model config is not a startup error: the site runs
	// without that feature and reports it through /api/session.
	for name, mutate := range map[string]func(*Config){
		"no model at all": func(c *Config) { c.BaseURL, c.APIKey, c.Model = "", "", "" },
		"no api key":      func(c *Config) { c.APIKey = "" },
		"no base url":     func(c *Config) { c.BaseURL = "" },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := NewServer(store, cfg, nil); err != nil {
			t.Fatalf("%s: NewServer refused to start: %v", name, err)
		}
	}
}

// lastRequest is defined on mockUpstream above.

// itoa is defined near the top of this file.
