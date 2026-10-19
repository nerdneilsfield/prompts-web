package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testMCPToken = "mcp-test-token-do-not-log"

// mcpEnv builds a running server with the MCP endpoint enabled.
func mcpEnv(t *testing.T, mutate func(*Config)) *testEnv {
	t.Helper()
	return newTestEnv(t, func(c *Config) {
		c.MCPToken = testMCPToken
		if mutate != nil {
			mutate(c)
		}
	})
}

// bearerTransport stamps the MCP token on every request, which is how a real
// remote client authenticates.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// connectMCP runs the real client handshake against the test server: initialize,
// protocol negotiation and the tool list come from the SDK, not from this test.
func connectMCP(t *testing.T, env *testEnv, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "prompts-web-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: env.server.URL + "/mcp",
		HTTPClient: &http.Client{
			Transport: bearerTransport{token: token},
		},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// callTool invokes a tool and fails on a transport or protocol error.
func callTool(t *testing.T, session *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// toolText returns the text content of a result.
func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("result has %d content blocks, want 1", len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content block is %T, want *mcp.TextContent", res.Content[0])
	}
	return text.Text
}

// decodeTool decodes a successful tool result.
func decodeTool[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool reported an error: %s", toolText(t, res))
	}
	return decode[T](t, []byte(toolText(t, res)))
}

// toolErrorCode returns the code and message of an IsError result.
func toolErrorCode(t *testing.T, res *mcp.CallToolResult) (string, string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("result is not an error: %s", toolText(t, res))
	}
	payload := decode[struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}](t, []byte(toolText(t, res)))
	if payload.Error.Code == "" || payload.Error.Message == "" {
		t.Fatalf("error result %q carries no code or message", toolText(t, res))
	}
	return payload.Error.Code, payload.Error.Message
}

// rpcErrorCode returns the JSON-RPC error code of a failed call.
func rpcErrorCode(t *testing.T, err error) int64 {
	t.Helper()
	var wireErr *jsonrpc.Error
	if !errors.As(err, &wireErr) {
		t.Fatalf("error %v is not a JSON-RPC error", err)
	}
	return wireErr.Code
}

// ---- endpoint gating ----

// TestMCPDisabledWithoutToken pins the off switch: without MCP_TOKEN /mcp must
// answer 404 and must not be answered by the SPA fallback, which would hand a
// JSON-RPC client index.html.
func TestMCPDisabledWithoutToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mcp-off.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	cfg := DefaultConfig()
	cfg.Password = testPassword
	cfg.DBPath = dbPath
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>app</html>")}}
	handler, err := NewServer(store, cfg, assets)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer anything")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("post /mcp: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "app") {
		t.Fatalf("the SPA answered /mcp: %s", raw)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodOptions, http.MethodPut, http.MethodPatch} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, "/mcp", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("disabled %s /mcp status = %d, want 404", method, rec.Code)
		}
	}

	// The asset tree still answers everything else.
	resp2, err := server.Client().Get(server.URL + "/")
	if err != nil {
		t.Fatalf("get /: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("root status = %d, want 200", resp2.StatusCode)
	}
}

// rawMCPPost sends one JSON-RPC initialize straight at /mcp, bypassing the SDK
// client, so a test can control headers and body size.
func rawMCPPost(t *testing.T, env *testEnv, headers map[string]string, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("post /mcp: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, raw
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"1"}}}`

// TestMCPRequiresBearerTokenIndependentOfTheCookie is the isolation test: a valid
// session cookie is worthless at /mcp, and only the bearer token opens it.
func TestMCPRequiresBearerTokenIndependentOfTheCookie(t *testing.T) {
	env := mcpEnv(t, nil)
	cookie := env.login(t)

	for name, headers := range map[string]map[string]string{
		"no credentials":     nil,
		"session cookie":     cookieHeader(cookie),
		"wrong bearer":       {"Authorization": "Bearer not-the-token"},
		"wrong scheme":       {"Authorization": "Basic " + testMCPToken},
		"empty bearer":       {"Authorization": "Bearer "},
		"token as cookie":    {"Cookie": "MCP_TOKEN=" + testMCPToken},
		"cookie and no auth": cookieHeader(cookie),
	} {
		resp, raw := rawMCPPost(t, env, headers, initializeBody)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401 (body %s)", name, resp.StatusCode, raw)
		}
	}

	resp, raw := rawMCPPost(t, env, map[string]string{"Authorization": "Bearer " + testMCPToken}, initializeBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid bearer: status = %d, body %s", resp.StatusCode, raw)
	}
}

// TestMCPOriginIsEnforced uses the existing allowlist: a browser-attached Origin
// that is not allowed is refused, an allowed one is not.
func TestMCPOriginIsEnforced(t *testing.T) {
	env := mcpEnv(t, nil)
	auth := map[string]string{"Authorization": "Bearer " + testMCPToken}

	resp, raw := rawMCPPost(t, env, map[string]string{
		"Authorization": auth["Authorization"],
		"Origin":        "https://evil.example.com",
	}, initializeBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad origin: status = %d, want 403 (body %s)", resp.StatusCode, raw)
	}
	if code := errorCode(t, raw); code != "bad_origin" {
		t.Fatalf("bad origin: code = %q, want bad_origin", code)
	}

	resp, raw = rawMCPPost(t, env, map[string]string{
		"Authorization": auth["Authorization"],
		"Origin":        testOrigin,
	}, initializeBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed origin: status = %d, body %s", resp.StatusCode, raw)
	}
}

// TestMCPBodyIsBounded reuses MaxBodyBytes: the MCP transport must reject an
// oversized request body instead of buffering it.
func TestMCPBodyIsBounded(t *testing.T) {
	env := mcpEnv(t, func(c *Config) { c.MaxBodyBytes = 512 })
	big := initializeBody + strings.Repeat(" ", 2048)
	resp, raw := rawMCPPost(t, env, map[string]string{"Authorization": "Bearer " + testMCPToken}, big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", resp.StatusCode, raw)
	}
}

// TestMCPWorksBehindSameHostProxy pins why the SDK's localhost guard is disabled:
// the default LISTEN_ADDR is loopback, and a reverse proxy on the same machine
// reaches it with the public Host header. The request must be served, with the
// token and origin as the boundary.
func TestMCPWorksBehindSameHostProxy(t *testing.T) {
	env := mcpEnv(t, nil)
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/mcp", strings.NewReader(initializeBody))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Host = "prompts.example.com" // what the proxy forwards
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+testMCPToken)
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("post /mcp: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, raw)
	}
}

// ---- protocol surface ----

func TestMCPInitializeAndToolList(t *testing.T) {
	env := mcpEnv(t, nil)
	session := connectMCP(t, env, testMCPToken)

	result := session.InitializeResult()
	if result == nil {
		t.Fatal("no initialize result")
	}
	if result.ServerInfo.Name != "prompts-web" {
		t.Fatalf("server name = %q, want prompts-web", result.ServerInfo.Name)
	}
	if result.Capabilities == nil || result.Capabilities.Tools == nil {
		t.Fatalf("server does not advertise tools: %+v", result.Capabilities)
	}

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{
		"create_prompt", "get_optimization", "get_prompt", "list_schemes",
		"optimize_prompt", "search_prompts", "update_prompt",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for _, tool := range tools.Tools {
		var schema map[string]any
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema for %s: %v", tool.Name, err)
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("schema for %s is not JSON: %v", tool.Name, err)
		}
		if schema["type"] != "object" {
			t.Fatalf("%s schema type = %v, want object", tool.Name, schema["type"])
		}
		if tool.Description == "" {
			t.Fatalf("%s has no description", tool.Name)
		}
	}
}

// TestMCPShapeErrorsAreProtocolErrors separates the two failure classes: a bad
// argument shape is a JSON-RPC InvalidParams error, and never a tool result.
func TestMCPShapeErrorsAreProtocolErrors(t *testing.T) {
	env := mcpEnv(t, nil)
	session := connectMCP(t, env, testMCPToken)

	cases := map[string]struct {
		tool string
		args any
	}{
		"limit above maximum":    {"search_prompts", map[string]any{"limit": 101}},
		"limit below minimum":    {"search_prompts", map[string]any{"limit": 0}},
		"limit wrong type":       {"search_prompts", map[string]any{"limit": "20"}},
		"limit null":             {"search_prompts", map[string]any{"limit": nil}},
		"query null":             {"search_prompts", map[string]any{"q": nil}},
		"tag item null":          {"create_prompt", map[string]any{"title": "t", "body": "b", "tags": []any{nil}}},
		"negative offset":        {"search_prompts", map[string]any{"offset": -1}},
		"unknown property":       {"search_prompts", map[string]any{"qurey": "x"}},
		"id missing":             {"get_prompt", map[string]any{}},
		"id wrong type":          {"get_prompt", map[string]any{"id": "one"}},
		"id not positive":        {"get_prompt", map[string]any{"id": 0}},
		"create missing body":    {"create_prompt", map[string]any{"title": "t"}},
		"create title wrong":     {"create_prompt", map[string]any{"title": 1, "body": "b"}},
		"create tags wrong":      {"create_prompt", map[string]any{"title": "t", "body": "b", "tags": "x"}},
		"update missing tags":    {"update_prompt", map[string]any{"id": 1, "title": "t", "body": "b"}},
		"update missing id":      {"update_prompt", map[string]any{"title": "t", "body": "b", "tags": []string{}}},
		"optimize missing input": {"optimize_prompt", map[string]any{"request_id": "r", "scheme_id": 1}},
		"scheme id wrong type":   {"optimize_prompt", map[string]any{"request_id": "r", "input": "i", "scheme_id": "1"}},
		"get optimization empty": {"get_optimization", map[string]any{}},
		"list schemes array":     {"list_schemes", []string{"x"}},
		"arguments not object":   {"search_prompts", "query"},
	}
	for name, tc := range cases {
		_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
		if err == nil {
			t.Fatalf("%s: %s accepted the arguments", name, tc.tool)
		}
		if code := rpcErrorCode(t, err); code != jsonrpc.CodeInvalidParams {
			t.Fatalf("%s: code = %d, want %d (InvalidParams)", name, code, jsonrpc.CodeInvalidParams)
		}
	}

	// Omitting the arguments object entirely is fine: it means "no arguments".
	if res := callTool(t, session, "list_schemes", nil); res.IsError {
		t.Fatalf("list_schemes with no arguments failed: %s", toolText(t, res))
	}
	// And a well-formed call that fails for a business reason is a tool result,
	// not a protocol error.
	res := callTool(t, session, "get_prompt", map[string]any{"id": 999999})
	if code, _ := toolErrorCode(t, res); code != "not_found" {
		t.Fatalf("code = %q, want not_found", code)
	}
}

// ---- prompts ----

func TestMCPPromptLifecycle(t *testing.T) {
	env := mcpEnv(t, nil)
	session := connectMCP(t, env, testMCPToken)

	created := decodeTool[Prompt](t, callTool(t, session, "create_prompt", map[string]any{
		"title": "总结文章",
		"body":  "请总结下面这篇文章",
		"tags":  []string{"总结", "写作"},
	}))
	if created.ID == 0 || created.Title != "总结文章" || len(created.Tags) != 2 {
		t.Fatalf("created prompt = %+v", created)
	}

	got := decodeTool[Prompt](t, callTool(t, session, "get_prompt", map[string]any{"id": created.ID}))
	if got.Body != "请总结下面这篇文章" || got.SourceRunID != nil {
		t.Fatalf("get prompt = %+v", got)
	}

	// Updating replaces the payload whole and keeps provenance untouched.
	updated := decodeTool[Prompt](t, callTool(t, session, "update_prompt", map[string]any{
		"id":    created.ID,
		"title": "改写总结",
		"body":  "改写后的正文",
		"tags":  []string{"改写"},
	}))
	if updated.Title != "改写总结" || updated.Body != "改写后的正文" || len(updated.Tags) != 1 || updated.Tags[0] != "改写" {
		t.Fatalf("updated prompt = %+v", updated)
	}
	if updated.CreatedAt != created.CreatedAt {
		t.Fatal("update changed created_at")
	}

	// Business validation keeps the REST message.
	if code, msg := toolErrorCode(t, callTool(t, session, "create_prompt", map[string]any{"title": "  ", "body": "b"})); code != "bad_request" || msg != "title must not be empty" {
		t.Fatalf("empty title: code = %q message = %q", code, msg)
	}
	if code, _ := toolErrorCode(t, callTool(t, session, "update_prompt", map[string]any{
		"id": 999999, "title": "t", "body": "b", "tags": []string{},
	})); code != "not_found" {
		t.Fatalf("update missing prompt: code = %q", code)
	}
	if code, _ := toolErrorCode(t, callTool(t, session, "create_prompt", map[string]any{
		"title": "t", "body": "b", "source_run_id": "no-such-run",
	})); code != "bad_source_run" {
		t.Fatalf("unknown source run: code = %q", code)
	}
}

// TestMCPSearchPaginatesInSQL checks the page contract: summaries carry no body,
// limit is clamped by the schema, and has_more marks a following page.
func TestMCPSearchPaginatesInSQL(t *testing.T) {
	env := mcpEnv(t, nil)
	session := connectMCP(t, env, testMCPToken)

	longBody := strings.Repeat("正", snippetRunes+50)
	var ids []int64
	for i := 0; i < 5; i++ {
		body := "正文" + itoa(int64(i))
		if i == 4 {
			body = longBody
		}
		p := decodeTool[Prompt](t, callTool(t, session, "create_prompt", map[string]any{
			"title": "条目" + itoa(int64(i)),
			"body":  body,
			"tags":  []string{"分页"},
		}))
		ids = append(ids, p.ID)
	}

	page := decodeTool[searchPromptsOutput](t, callTool(t, session, "search_prompts", map[string]any{"tag": "分页", "limit": 2}))
	if len(page.Prompts) != 2 || !page.HasMore {
		t.Fatalf("first page = %+v", page)
	}
	if page.Limit != 2 || page.Offset != 0 {
		t.Fatalf("page limits = %+v", page)
	}
	// Newest first, and the body that runs past the snippet limit is cut there.
	if page.Prompts[0].ID != ids[4] {
		t.Fatalf("first row = %d, want %d", page.Prompts[0].ID, ids[4])
	}
	if got := len([]rune(page.Prompts[0].Snippet)); got != snippetRunes {
		t.Fatalf("snippet length = %d, want %d", got, snippetRunes)
	}
	if page.Prompts[1].Snippet != "正文3" {
		t.Fatalf("short body was truncated: %q", page.Prompts[1].Snippet)
	}

	// Newest first, and the row that hit the snippet limit is truncated.
	raw := toolText(t, callTool(t, session, "search_prompts", map[string]any{"tag": "分页", "limit": 2}))
	var envelope map[string]any
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("search result is not JSON: %v", err)
	}
	for _, item := range envelope["prompts"].([]any) {
		if _, ok := item.(map[string]any)["body"]; ok {
			t.Fatalf("summary carries a body: %v", item)
		}
	}

	last := decodeTool[searchPromptsOutput](t, callTool(t, session, "search_prompts", map[string]any{"tag": "分页", "limit": 2, "offset": 4}))
	if len(last.Prompts) != 1 || last.HasMore {
		t.Fatalf("last page = %+v", last)
	}
	empty := decodeTool[searchPromptsOutput](t, callTool(t, session, "search_prompts", map[string]any{"q": "没有这个词"}))
	if len(empty.Prompts) != 0 || empty.Prompts == nil || empty.HasMore {
		t.Fatalf("empty page = %+v", empty)
	}

	selected := decodeTool[searchPromptsOutput](t, callTool(t, session, "search_prompts", map[string]any{"q": "条目3", "limit": 100}))
	if len(selected.Prompts) != 1 || selected.Prompts[0].ID != ids[3] {
		t.Fatalf("query page = %+v", selected)
	}
}

func TestMCPListSchemesReturnsFullBody(t *testing.T) {
	env := mcpEnv(t, nil)
	session := connectMCP(t, env, testMCPToken)
	scheme, err := env.store.CreateScheme(Scheme{Name: "精简", Body: "去掉冗余表达，保留事实。"})
	if err != nil {
		t.Fatalf("CreateScheme: %v", err)
	}
	schemes := decodeTool[[]Scheme](t, callTool(t, session, "list_schemes", nil))
	if len(schemes) != 1 || schemes[0].ID != scheme.ID || schemes[0].Body != "去掉冗余表达，保留事实。" {
		t.Fatalf("schemes = %+v", schemes)
	}
}

// ---- optimization ----

// TestMCPOptimizeDoesNotTouchPrompts is the core promise of the tool: a run is
// recorded, no prompt is created or overwritten, and a replay costs nothing.
func TestMCPOptimizeDoesNotTouchPrompts(t *testing.T) {
	env := mcpEnv(t, nil)
	session := connectMCP(t, env, testMCPToken)
	scheme, err := env.store.CreateScheme(Scheme{Name: testSchemeName, Body: testSchemeBody})
	if err != nil {
		t.Fatalf("CreateScheme: %v", err)
	}
	existing, err := env.store.CreatePrompt(Prompt{Title: "既有条目", Body: "既有正文", Tags: []string{"保留"}})
	if err != nil {
		t.Fatalf("CreatePrompt: %v", err)
	}

	run := decodeTool[Run](t, callTool(t, session, "optimize_prompt", map[string]any{
		"request_id": "mcp-run-1",
		"input":      "原文内容",
		"scheme_id":  scheme.ID,
	}))
	if run.Status != RunSucceeded || run.Result != "优化结果" || run.SchemeID != scheme.ID {
		t.Fatalf("run = %+v", run)
	}

	// Replay: same id and payload returns the stored run without another call.
	replay := decodeTool[Run](t, callTool(t, session, "optimize_prompt", map[string]any{
		"request_id": "mcp-run-1",
		"input":      "原文内容",
		"scheme_id":  scheme.ID,
	}))
	if replay.Result != run.Result || replay.CreatedAt != run.CreatedAt {
		t.Fatalf("replay = %+v, first = %+v", replay, run)
	}
	if got := env.upstream.calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}

	// A different payload under the same id is a conflict, still with one call.
	if code, _ := toolErrorCode(t, callTool(t, session, "optimize_prompt", map[string]any{
		"request_id": "mcp-run-1",
		"input":      "另一段原文",
		"scheme_id":  scheme.ID,
	})); code != "request_id_conflict" {
		t.Fatalf("conflict code = %q", code)
	}
	if got := env.upstream.calls.Load(); got != 1 {
		t.Fatalf("upstream calls after conflict = %d, want 1", got)
	}

	// Nothing was written to the library: the prompt count and the existing row
	// are unchanged.
	after, err := env.store.Prompt(existing.ID)
	if err != nil {
		t.Fatalf("Prompt after run: %v", err)
	}
	if after.Title != "既有条目" || after.Body != "既有正文" {
		t.Fatalf("optimization mutated a prompt: %+v", after)
	}
	all, err := env.store.Prompts("", "")
	if err != nil {
		t.Fatalf("Prompts: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("prompt count = %d, want 1", len(all))
	}

	// The run is readable by id, and the prompt can now be created from it.
	fetched := decodeTool[Run](t, callTool(t, session, "get_optimization", map[string]any{"request_id": "mcp-run-1"}))
	if fetched.RequestID != "mcp-run-1" || fetched.Result != "优化结果" {
		t.Fatalf("fetched run = %+v", fetched)
	}
	fromRun := decodeTool[Prompt](t, callTool(t, session, "create_prompt", map[string]any{
		"title":         "来自运行",
		"body":          fetched.Result,
		"source_run_id": "mcp-run-1",
	}))
	if fromRun.SourceRunID == nil || *fromRun.SourceRunID != "mcp-run-1" {
		t.Fatalf("created prompt = %+v", fromRun)
	}

	// Updating that prompt leaves its provenance alone.
	updated := decodeTool[Prompt](t, callTool(t, session, "update_prompt", map[string]any{
		"id": fromRun.ID, "title": "改名", "body": "新正文", "tags": []string{},
	}))
	if updated.SourceRunID == nil || *updated.SourceRunID != "mcp-run-1" {
		t.Fatalf("update dropped provenance: %+v", updated)
	}

	if code, _ := toolErrorCode(t, callTool(t, session, "get_optimization", map[string]any{"request_id": "missing"})); code != "not_found" {
		t.Fatalf("missing run code = %q", code)
	}
}

func TestMCPOptimizeFailures(t *testing.T) {
	env := mcpEnv(t, nil)
	session := connectMCP(t, env, testMCPToken)
	scheme, err := env.store.CreateScheme(Scheme{Name: testSchemeName, Body: testSchemeBody})
	if err != nil {
		t.Fatalf("CreateScheme: %v", err)
	}

	// Upstream rejects the key: the run is recorded as failed, and the call
	// succeeds, exactly like the REST endpoint returning 200 with status=failed.
	env.upstream.replyJSON(http.StatusUnauthorized, `{"error":"bad key"}`)
	run := decodeTool[Run](t, callTool(t, session, "optimize_prompt", map[string]any{
		"request_id": "mcp-fail-1", "input": "原文", "scheme_id": scheme.ID,
	}))
	if run.Status != RunFailed || run.ErrorKind != ErrKindAuth {
		t.Fatalf("auth failure run = %+v", run)
	}

	// Business failures are tool errors with a stable code.
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"unknown scheme": {args: map[string]any{"request_id": "mcp-fail-2", "input": "原文", "scheme_id": 999999}, want: "bad_scheme"},
		"empty input":    {args: map[string]any{"request_id": "mcp-fail-3", "input": "   ", "scheme_id": scheme.ID}, want: "bad_request"},
	} {
		code, _ := toolErrorCode(t, callTool(t, session, "optimize_prompt", tc.args))
		if code != tc.want {
			t.Fatalf("%s: code = %q, want %q", name, code, tc.want)
		}
	}
}

func TestMCPOptimizeUnconfiguredModel(t *testing.T) {
	env := mcpEnv(t, func(c *Config) {
		c.BaseURL, c.APIKey, c.Model = "", "", ""
	})
	session := connectMCP(t, env, testMCPToken)
	scheme, err := env.store.CreateScheme(Scheme{Name: testSchemeName, Body: testSchemeBody})
	if err != nil {
		t.Fatalf("CreateScheme: %v", err)
	}
	code, _ := toolErrorCode(t, callTool(t, session, "optimize_prompt", map[string]any{
		"request_id": "mcp-unconfigured", "input": "原文", "scheme_id": scheme.ID,
	}))
	if code != "model_not_configured" {
		t.Fatalf("code = %q, want model_not_configured", code)
	}
	if got := env.upstream.calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
	if _, err := env.store.Run("mcp-unconfigured"); err == nil {
		t.Fatal("an unconfigured model still inserted a run row")
	}
}

// TestMCPOptimizeTimeout checks that the per-call deadline reaches the handler:
// the run must settle as timeout rather than hang or succeed.
func TestMCPOptimizeTimeout(t *testing.T) {
	env := mcpEnv(t, func(c *Config) { c.RequestTimeout = 150 * time.Millisecond })
	session := connectMCP(t, env, testMCPToken)
	scheme, err := env.store.CreateScheme(Scheme{Name: testSchemeName, Body: testSchemeBody})
	if err != nil {
		t.Fatalf("CreateScheme: %v", err)
	}
	env.upstream.reply(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"太晚了"}}]}`)
	})
	run := decodeTool[Run](t, callTool(t, session, "optimize_prompt", map[string]any{
		"request_id": "mcp-timeout", "input": "原文", "scheme_id": scheme.ID,
	}))
	if run.Status != RunFailed || run.ErrorKind != ErrKindTimeout {
		t.Fatalf("timeout run = %+v", run)
	}
	if run.Result != "" {
		t.Fatalf("a timed-out run carries a result: %+v", run)
	}
}

// TestMCPConcurrentSameRequestIDCallsUpstreamOnce is the dedup guarantee over
// MCP: concurrent duplicates share one upstream call and one run row.
func TestMCPConcurrentSameRequestIDCallsUpstreamOnce(t *testing.T) {
	env := mcpEnv(t, nil)
	scheme, err := env.store.CreateScheme(Scheme{Name: testSchemeName, Body: testSchemeBody})
	if err != nil {
		t.Fatalf("CreateScheme: %v", err)
	}
	release := make(chan struct{})
	env.upstream.reply(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"唯一结果"}}]}`)
	})

	const racers = 6
	type outcome struct {
		result *mcp.CallToolResult
		err    error
	}
	results := make([]outcome, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// One session per racer: a stateless server treats every POST as its
			// own session anyway, and this keeps the test free of client-side
			// ordering assumptions.
			client := mcp.NewClient(&mcp.Implementation{Name: "racer", Version: "1"}, nil)
			session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
				Endpoint:             env.server.URL + "/mcp",
				HTTPClient:           &http.Client{Transport: bearerTransport{token: testMCPToken}},
				DisableStandaloneSSE: true,
			}, nil)
			if err != nil {
				results[i].err = err
				return
			}
			defer session.Close()
			<-start
			results[i].result, results[i].err = session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "optimize_prompt",
				Arguments: map[string]any{
					"request_id": "mcp-race",
					"input":      "原文",
					"scheme_id":  scheme.ID,
				},
			})
		}(i)
	}
	close(start)
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := env.upstream.calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want exactly 1", got)
	}
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("racer %d: %v", i, r.err)
		}
		if r.result.IsError {
			t.Fatalf("racer %d got a tool error: %s", i, toolText(t, r.result))
		}
		run := decodeTool[Run](t, r.result)
		if run.RequestID != "mcp-race" || run.SchemeID != scheme.ID {
			t.Fatalf("racer %d got run %+v", i, run)
		}
		if run.Status != RunSucceeded && run.Status != RunRunning {
			t.Fatalf("racer %d status = %q", i, run.Status)
		}
	}
	// Exactly one racer owns the settled result.
	stored, err := env.store.Run("mcp-race")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stored.Status != RunSucceeded || stored.Result != "唯一结果" {
		t.Fatalf("stored run = %+v", stored)
	}
}

// TestMCPLogsNoSecrets asserts the MCP path never writes the token or the model
// key, including on failures.
func TestMCPLogsNoSecrets(t *testing.T) {
	env := mcpEnv(t, nil)
	session := connectMCP(t, env, testMCPToken)
	scheme, err := env.store.CreateScheme(Scheme{Name: testSchemeName, Body: testSchemeBody})
	if err != nil {
		t.Fatalf("CreateScheme: %v", err)
	}
	env.upstream.replyJSON(http.StatusForbidden, `{"error":"bad key"}`)
	callTool(t, session, "optimize_prompt", map[string]any{
		"request_id": "mcp-log", "input": "秘密原文", "scheme_id": scheme.ID,
	})
	callTool(t, session, "get_prompt", map[string]any{"id": 1})

	logs := env.logs.String()
	for _, secret := range []string{testMCPToken, testAPIKey, "秘密原文"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("log output contains %q:\n%s", secret, logs)
		}
	}
}
