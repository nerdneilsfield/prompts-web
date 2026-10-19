package app

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultSearchLimit is the page size used when the caller omits limit. The
// allowed range itself lives in the tool's schema.
const defaultSearchLimit = 20

const mcpInstructions = `这是自托管的 Prompt 库与优化工作台。search_prompts 只返回摘要（标题、标签、正文前 200 字），
用 get_prompt 取全文。optimize_prompt 调用上游模型改写原文并把结果记成一次运行，它不会保存或覆盖任何已有 Prompt；
要把结果存进库，用 create_prompt（可带 source_run_id 指向那次运行）。request_id 由你生成并复用：同一个 id
重发只会重放已记录的结果，不会二次调用模型。`

// newMCPHandler builds the Streamable HTTP endpoint backed by the tool server.
// It is stateless: no session is retained between requests, so the bearer token
// is the only identity and two concurrent clients cannot share state.
func newMCPHandler(c *Client) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "prompts-web", Title: "Prompt 库", Version: "1"}, &mcp.ServerOptions{
		Instructions: mcpInstructions,
		// No logger: the SDK would log transport details, and nothing here may
		// write the bearer token or request text anywhere.
	})
	c.addMCPTools(server)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		MaxRequestBodyBytes:          c.cfg.maxBodyBytes(),
		PropagateRequestCancellation: true,
		// The SDK's localhost guard rejects a loopback *listener* reached with a
		// non-loopback Host header, which is exactly the shape of a reverse proxy
		// on the same machine (the default LISTEN_ADDR is 127.0.0.1). See
		// TestMCPWorksBehindSameHostProxy. It adds nothing here: a local-only
		// server is already protected by the bearer token and the origin check,
		// which a rebinding page cannot satisfy.
		DisableLocalhostProtection: true,
	})
}

// handleMCP gates the endpoint. A bearer token replaces the session cookie here:
// cookies are ambient credentials a cross-site request can carry, so they are
// never consulted. The origin rule is the one REST writes already use.
func (c *Client) handleMCP(w http.ResponseWriter, r *http.Request) {
	if c.mcp == nil {
		http.NotFound(w, r)
		return
	}
	if !bearerTokenOK(r.Header.Get("Authorization"), c.cfg.MCPToken) {
		fail(w, http.StatusUnauthorized, "unauthorized", "bearer token required")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !c.originAllowed(origin) {
		fail(w, http.StatusForbidden, "bad_origin", "origin not allowed")
		return
	}
	c.mcp.ServeHTTP(w, r)
}

// bearerTokenOK compares the presented token in constant time. A missing,
// differently-schemed or empty header never matches.
func bearerTokenOK(header, token string) bool {
	const scheme = "Bearer "
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(scheme):]), []byte(token)) == 1
}

// ---- tools ----

func (c *Client) addMCPTools(s *mcp.Server) {
	s.AddTool(&mcp.Tool{
		Name:        "search_prompts",
		Description: "分页检索 Prompt 库，返回摘要（不含正文）。q 匹配标题与正文的子串，tag 精确匹配标签，两者可叠加。",
		InputSchema: toolSearchPromptsInput.raw,
	}, c.toolSearchPrompts)
	s.AddTool(&mcp.Tool{
		Name:        "get_prompt",
		Description: "按 id 取一条 Prompt 的全文，含标签与来源运行 id。",
		InputSchema: toolGetPromptInput.raw,
	}, c.toolGetPrompt)
	s.AddTool(&mcp.Tool{
		Name:        "create_prompt",
		Description: "新建一条 Prompt。source_run_id 可选，给出时必须指向一次已成功的优化运行。",
		InputSchema: toolCreatePromptInput.raw,
	}, c.toolCreatePrompt)
	s.AddTool(&mcp.Tool{
		Name:        "update_prompt",
		Description: "整体替换一条 Prompt 的标题、正文与标签（四个字段都必填）。来源运行 id 与创建时间不会被改写。",
		InputSchema: toolUpdatePromptInput.raw,
	}, c.toolUpdatePrompt)
	s.AddTool(&mcp.Tool{
		Name:        "list_schemes",
		Description: "列出全部优化方案，含方案正文。optimize_prompt 的 scheme_id 来自这里。",
		InputSchema: toolListSchemesInput.raw,
	}, c.toolListSchemes)
	s.AddTool(&mcp.Tool{
		Name:        "optimize_prompt",
		Description: "用指定方案改写原文，同步等待模型返回（上限 90 秒）。结果只写进运行记录，不会保存或覆盖任何 Prompt。request_id 是去重键：同 id 同参数重发只重放已记录的结果。",
		InputSchema: toolOptimizePromptInput.raw,
	}, c.toolOptimizePrompt)
	s.AddTool(&mcp.Tool{
		Name:        "get_optimization",
		Description: "按 request_id 查询一次优化运行，用于响应丢失后恢复。status 为 running 表示另一次请求仍在执行。",
		InputSchema: toolGetOptimizationInput.raw,
	}, c.toolGetOptimization)
}

// bind validates the raw arguments against the tool's advertised schema and, only
// then, decodes them into the typed argument struct. Validation uses the same
// resolved schema the client was given, so shape and type errors cannot slip
// through a decoder that is more permissive than the contract.
//
// This is the one place the typed mcp.AddTool helper is not used: it turns a
// schema violation into an IsError result, and this endpoint reports shape and
// type violations as JSON-RPC InvalidParams with business failures as IsError.
func (in *mcpToolInput) bind(req *mcp.CallToolRequest, v any) error {
	var raw []byte
	if req.Params != nil {
		raw = req.Params.Arguments
	}
	var instance any = map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &instance); err != nil {
			return errors.New("arguments are not valid JSON")
		}
	}
	if err := in.resolved.Validate(instance); err != nil {
		return err
	}
	if len(raw) == 0 || v == nil {
		return nil
	}
	return json.Unmarshal(raw, v)
}

func invalidParams(err error) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
}

// jsonResult renders a value as the tool's text content.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "encoding result failed"}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
}

// errorResult renders a business failure. The code and message are the same
// vocabulary the HTTP API uses; both are safe to hand to a client.
func errorResult(code, message string) *mcp.CallToolResult {
	raw, err := json.Marshal(map[string]apiError{"error": {Code: code, Message: message}})
	if err != nil {
		raw = []byte(`{"error":{"code":"internal","message":"internal error"}}`)
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
	}
}

// internalToolError logs the real error and reports the generic failure: the
// detail may quote request text or a database path and must not reach the client.
func (c *Client) internalToolError(context string, err error) (*mcp.CallToolResult, error) {
	c.log.Printf("error: mcp %s: %v", context, err)
	return errorResult("internal", "internal error"), nil
}

type searchPromptsArgs struct {
	Q      string `json:"q"`
	Tag    string `json:"tag"`
	Limit  *int   `json:"limit"`
	Offset *int   `json:"offset"`
}

type searchPromptsOutput struct {
	Prompts []PromptSummary `json:"prompts"`
	Limit   int             `json:"limit"`
	Offset  int             `json:"offset"`
	HasMore bool            `json:"has_more"`
}

func (c *Client) toolSearchPrompts(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args searchPromptsArgs
	if err := toolSearchPromptsInput.bind(req, &args); err != nil {
		return nil, invalidParams(err)
	}
	limit := defaultSearchLimit
	if args.Limit != nil {
		limit = *args.Limit
	}
	offset := 0
	if args.Offset != nil {
		offset = *args.Offset
	}
	prompts, hasMore, err := c.store.SearchPrompts(args.Q, args.Tag, limit, offset)
	if err != nil {
		return c.internalToolError("search prompts", err)
	}
	return jsonResult(searchPromptsOutput{Prompts: prompts, Limit: limit, Offset: offset, HasMore: hasMore})
}

type promptIDArgs struct {
	ID int64 `json:"id"`
}

func (c *Client) toolGetPrompt(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args promptIDArgs
	if err := toolGetPromptInput.bind(req, &args); err != nil {
		return nil, invalidParams(err)
	}
	prompt, err := c.store.Prompt(args.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return errorResult("not_found", "prompt not found"), nil
	}
	if err != nil {
		return c.internalToolError("get prompt", err)
	}
	return jsonResult(prompt)
}

type createPromptArgs struct {
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags"`
	SourceRunID *string  `json:"source_run_id"`
}

func (c *Client) toolCreatePrompt(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args createPromptArgs
	if err := toolCreatePromptInput.bind(req, &args); err != nil {
		return nil, invalidParams(err)
	}
	payload := promptPayload{Title: args.Title, Body: args.Body, Tags: args.Tags, SourceRunID: args.SourceRunID}
	if err := normalizePrompt(&payload); err != nil {
		return errorResult("bad_request", err.Error()), nil
	}
	prompt, err := c.store.CreatePrompt(Prompt{
		Title:       payload.Title,
		Body:        payload.Body,
		Tags:        payload.Tags,
		SourceRunID: payload.SourceRunID,
	})
	if errors.Is(err, ErrSourceRun) {
		return errorResult("bad_source_run", "source run does not exist or did not succeed"), nil
	}
	if err != nil {
		return c.internalToolError("create prompt", err)
	}
	return jsonResult(prompt)
}

// updatePromptArgs mirrors the REST PUT: the payload is replaced whole, so every
// field is required. Reading the row and merging a partial payload would race a
// concurrent edit and could silently keep a stale body.
type updatePromptArgs struct {
	ID    int64    `json:"id"`
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags"`
}

func (c *Client) toolUpdatePrompt(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args updatePromptArgs
	if err := toolUpdatePromptInput.bind(req, &args); err != nil {
		return nil, invalidParams(err)
	}
	payload := promptPayload{Title: args.Title, Body: args.Body, Tags: args.Tags}
	if err := normalizePrompt(&payload); err != nil {
		return errorResult("bad_request", err.Error()), nil
	}
	// UpdatePrompt never writes source_run_id: provenance stays as recorded.
	prompt, err := c.store.UpdatePrompt(Prompt{ID: args.ID, Title: payload.Title, Body: payload.Body, Tags: payload.Tags})
	if errors.Is(err, sql.ErrNoRows) {
		return errorResult("not_found", "prompt not found"), nil
	}
	if err != nil {
		return c.internalToolError("update prompt", err)
	}
	return jsonResult(prompt)
}

func (c *Client) toolListSchemes(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if err := toolListSchemesInput.bind(req, nil); err != nil {
		return nil, invalidParams(err)
	}
	schemes, err := c.store.Schemes()
	if err != nil {
		return c.internalToolError("list schemes", err)
	}
	return jsonResult(schemes)
}

type optimizePromptArgs struct {
	RequestID string `json:"request_id"`
	Input     string `json:"input"`
	SchemeID  int64  `json:"scheme_id"`
}

func (c *Client) toolOptimizePrompt(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args optimizePromptArgs
	if err := toolOptimizePromptInput.bind(req, &args); err != nil {
		return nil, invalidParams(err)
	}
	run, failure := c.executeOptimization(ctx, optimizeRequest{
		RequestID: args.RequestID,
		Input:     args.Input,
		SchemeID:  args.SchemeID,
	})
	if failure != nil {
		return errorResult(failure.code, failure.message), nil
	}
	// A run still marked running means a concurrent request owns the upstream
	// call; its status is what the client needs, so it is a successful result.
	return jsonResult(run)
}

type requestIDArgs struct {
	RequestID string `json:"request_id"`
}

func (c *Client) toolGetOptimization(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args requestIDArgs
	if err := toolGetOptimizationInput.bind(req, &args); err != nil {
		return nil, invalidParams(err)
	}
	run, err := c.store.Run(args.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return errorResult("not_found", "run not found"), nil
	}
	if err != nil {
		return c.internalToolError("get run", err)
	}
	return jsonResult(run)
}
