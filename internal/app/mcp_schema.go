package app

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// mcpToolInput couples a tool's advertised schema with its resolved form. The
// advertised schema is the contract an agent reads; the resolved one is what the
// handler enforces, so the two cannot drift the way a hand-written decoder would.
type mcpToolInput struct {
	raw      json.RawMessage
	resolved *jsonschema.Resolved
}

// newToolInput parses and resolves one schema. A malformed or unresolvable schema
// is a code defect in a constant below, not a runtime condition, so it fails
// loudly at startup.
func newToolInput(raw string) *mcpToolInput {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		panic("mcp tool schema is not valid JSON: " + err.Error())
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		panic("mcp tool schema does not resolve: " + err.Error())
	}
	return &mcpToolInput{raw: json.RawMessage(raw), resolved: resolved}
}

var (
	toolSearchPromptsInput = newToolInput(`{
  "type": "object",
  "properties": {
    "q": {"type": "string", "maxLength": 500, "description": "标题或正文的子串，大小写不敏感（仅 ASCII）。"},
    "tag": {"type": "string", "maxLength": 200, "description": "精确匹配的单个标签，区分大小写。"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 100, "default": 20, "description": "每页条数。"},
    "offset": {"type": "integer", "minimum": 0, "default": 0, "description": "跳过的条数。"}
  },
  "additionalProperties": false
}`)

	toolGetPromptInput = newToolInput(`{
  "type": "object",
  "properties": {
    "id": {"type": "integer", "minimum": 1, "description": "Prompt 的 id。"}
  },
  "required": ["id"],
  "additionalProperties": false
}`)

	toolCreatePromptInput = newToolInput(`{
  "type": "object",
  "properties": {
    "title": {"type": "string", "description": "标题，不能为空。"},
    "body": {"type": "string", "description": "正文，不能为空。"},
    "tags": {"type": "array", "items": {"type": "string"}, "description": "最多 32 个标签，单个最长 64 字符。"},
    "source_run_id": {"type": "string", "description": "可选，来源优化运行的 request_id，必须已成功。"}
  },
  "required": ["title", "body"],
  "additionalProperties": false
}`)

	toolUpdatePromptInput = newToolInput(`{
  "type": "object",
  "properties": {
    "id": {"type": "integer", "minimum": 1, "description": "Prompt 的 id。"},
    "title": {"type": "string", "description": "新标题，不能为空。"},
    "body": {"type": "string", "description": "新正文，不能为空。"},
    "tags": {"type": "array", "items": {"type": "string"}, "description": "新的完整标签列表；空数组表示清空标签。"}
  },
  "required": ["id", "title", "body", "tags"],
  "additionalProperties": false
}`)

	toolListSchemesInput = newToolInput(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)

	toolOptimizePromptInput = newToolInput(`{
  "type": "object",
  "properties": {
    "request_id": {"type": "string", "minLength": 1, "maxLength": 128, "pattern": "^[A-Za-z0-9._-]+$", "description": "去重键，1-128 个 [A-Za-z0-9._-] 字符，由调用方生成。"},
    "input": {"type": "string", "description": "待优化的原文，不能为空。"},
    "scheme_id": {"type": "integer", "minimum": 1, "description": "方案 id，来自 list_schemes。"}
  },
  "required": ["request_id", "input", "scheme_id"],
  "additionalProperties": false
}`)

	toolGetOptimizationInput = newToolInput(`{
  "type": "object",
  "properties": {
    "request_id": {"type": "string", "description": "优化运行的 request_id。"}
  },
  "required": ["request_id"],
  "additionalProperties": false
}`)
)
