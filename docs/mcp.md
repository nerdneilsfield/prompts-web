# MCP 远程接入

服务在 `/mcp` 上挂了一个 MCP 端点（Streamable HTTP），让支持 MCP 的客户端直接读写 Prompt 库、读取方案、发起优化。
它和网页、REST 接口共用同一个进程、同一个 SQLite 文件、同一套校验与优化执行路径。

**默认关闭。** 没有设置 `MCP_TOKEN` 时 `/mcp` 对任何请求都返回 404，也不会回落到前端静态页。

本页只讲 MCP 侧的配置和契约；REST 接口见 [`api.md`](api.md)。

## 开启

先生成一条随机令牌，再把它写进部署环境：

```bash
openssl rand -hex 32
```

```bash
# .env 或容器环境里
MCP_TOKEN=<上一步输出的那串值>
```

| 变量 | 默认 | 说明 |
|---|---|---|
| `MCP_TOKEN` | 空 | MCP 客户端的 Bearer 令牌。空表示不启用端点。修改后需重启进程生效。 |

几点约束：

- 令牌是**独立凭据**，与会话 Cookie、`APP_PASSWORD` 互不相干。不要复用站点密码：MCP 客户端会把令牌写在配置文件里。
- 每个 `/mcp` 请求都必须带 `Authorization: Bearer <MCP_TOKEN>`，比较是常数时间的。带了合法会话 Cookie 但没带令牌同样返回 401。
- 浏览器会自动附带的凭据在这里一律不认，所以令牌不会被跨站请求顺手带上。
- 请求带 `Origin` 时，必须与 `APP_ORIGIN` 推导出的允许来源一致，否则 403 `bad_origin`。命令行客户端不带 `Origin`，不受影响。
- 请求体上限沿用 `MaxBodyBytes`（默认 1 MiB），超出返回 413。
- 端点是**无状态**的：不保存 MCP 会话，响应是 `application/json` 而不是 SSE 流。每次请求独立鉴权，所以重启后客户端不用重新握手，令牌不变即可继续用。
  正在执行的 `optimize_prompt` 不受这条影响：收到 SIGTERM 时进程会等挂起的运行返回（最长约 `RequestTimeout` + 10 秒）。
  若进程被强制杀死，留下未结算的运行会在下次启动时变为 `status=failed`、`error_kind=interrupted`（用 `get_optimization` 能查到），换一个新的 `request_id` 重跑即可。
- 令牌、站点密码、模型密钥都不写日志；工具错误只回固定的错误码和消息，数据库错误与上游错误只进服务端日志。

## 工具

七个工具，没有删除类工具。参数校验由官方 SDK 的 JSON Schema 校验器按每个工具公布的 schema 执行，业务校验与 REST 共用同一段代码。

| 工具 | 参数 | 返回 |
|---|---|---|
| `search_prompts` | `q`、`tag`、`limit`（默认 20，上限 100）、`offset`（≥0） | `{prompts:[{id,title,tags,snippet,updated_at}],limit,offset,has_more}`，正文只给前 200 字摘要 |
| `get_prompt` | `id` | 完整 Prompt（含正文、标签、`source_run_id`） |
| `create_prompt` | `title`、`body`、`tags`、可选 `source_run_id` | 新建的 Prompt |
| `update_prompt` | `id`、`title`、`body`、`tags`（四个全必填） | 更新后的 Prompt；`source_run_id` 与 `created_at` 不变 |
| `list_schemes` | 无 | 全部方案，含正文 |
| `optimize_prompt` | `request_id`、`input`、`scheme_id` | Run 对象（见 [`api.md`](api.md#优化运行)） |
| `get_optimization` | `request_id` | Run 对象 |

`search_prompts` 的分页在 SQL 里完成（`LIMIT/OFFSET`），摘要不带正文：先搜再取，别把整个库拉进上下文。

`update_prompt` 是整体替换，语义等同 `PUT /api/prompts/{id}`。要保留原值就把值原样传回来（先 `get_prompt`）。

`optimize_prompt` 的执行与 REST 的 `POST /api/optimizations` 完全相同：同一套校验、同一条去重路径、同样的终态分类。

- 它会**记一次运行，不会保存或覆盖任何 Prompt**。要把结果入库，另外调 `create_prompt`（可带 `source_run_id` 指向这次运行）。
- `request_id` 是去重键。同 id 同参数重发只重放已记录的结果，不会再调模型、不产生二次上游成本；同 id 但 `input` 或 `scheme_id` 不同是 `request_id_conflict`。
- 若同 id 的另一次请求仍在执行，返回的 Run 里 `status` 是 `running`（这不算错误）。
- 同步等待模型返回，上限 `RequestTimeout`（90 秒）。超时的运行 `status=failed`、`error_kind=timeout`，可用 `get_optimization` 复查。
- 模型未配置时返回 `model_not_configured`，不调用上游、不写运行记录。

## 两类错误

客户端要区分它们：

| 类别 | 形式 | 例子 |
|---|---|---|
| 参数不符合公布的 schema | JSON-RPC 协议错误，`code = -32602`（InvalidParams） | 缺少必填字段、类型不对（`id: "1"`、`q: null`、`tags: [null]`）、多出未知字段、`limit` 超出 1–100、`request_id` 含 `[A-Za-z0-9._-]` 之外的字符、参数不是对象 |
| 业务失败 | 工具结果，`isError: true`，正文是 `{"error":{"code":...,"message":...}}` | `not_found`、`bad_request`（标题为空、正文全空白等）、`bad_scheme`、`bad_source_run`、`model_not_configured`、`request_id_conflict`、`internal` |

错误码与 REST 的错误码是同一套词汇，含义见 [`api.md`](api.md#错误码)。`internal` 不含任何内部细节。

## 客户端配置

### Pi

```bash
export PROMPTS_MCP_TOKEN='与 MCP_TOKEN 相同的值'
pi mcp add prompts --url https://prompts.example.com/mcp \
  --bearer-token-env-var PROMPTS_MCP_TOKEN
pi mcp list          # 能看到 prompts 与它的 7 个工具即接通
```

或者直接在 `~/.pi/agent/mcp.json` 里写条目。注意 `timeout` 是**秒**，默认 60 秒比服务端 90 秒的优化上限短，跑优化时必须调高：

```json
{
  "mcpServers": {
    "prompts": {
      "url": "https://prompts.example.com/mcp",
      "headers": { "Authorization": "Bearer ${PROMPTS_MCP_TOKEN}" },
      "timeout": 120,
      "description": "自托管 Prompt 库：检索、读写 Prompt，列方案，执行优化"
    }
  }
}
```

工具名形如 `mcp__prompts__search_prompts`。Pi 只支持 Streamable HTTP，不支持旧的 SSE 传输；本端点就是前者。

### 其他客户端

任何支持 Streamable HTTP 的客户端都可以接，填 URL `https://<你的域名>/mcp`，加 `Authorization: Bearer <MCP_TOKEN>`。
不需要 OAuth，本端点也不提供；没有会话建立步骤需要手工完成。

先用 curl 确认端点通了（`initialize` 是普通 JSON-RPC POST）：

```bash
curl -sS https://prompts.example.com/mcp \
  -H "Authorization: Bearer $MCP_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'
```

返回 JSON（`serverInfo.name` 为 `prompts-web`）即成功；401 是令牌不对，404 是服务端没设 `MCP_TOKEN`，403 是 `Origin` 不在允许列表。

## 部署注意

- 反向代理要放行 `POST /mcp`，并保持请求体上限不低于 `MaxBodyBytes`。`deploy/Caddyfile` 的 2 MiB 外层限制已经够用，无需为新端点单独加规则。
- 反向代理的读写超时不要低于 `RequestTimeout`：`optimize_prompt` 是同步调用，最长 90 秒。
- 应用端口发布到公网时，MCP 的边界就只有令牌本身。端口只对代理开放、或只在局域网内运行时更稳。
- 与前端同域（`APP_ORIGIN`）部署最省事；跨域浏览器客户端不会工作，本端点的 `Origin` 校验只认 `APP_ORIGIN` 那一个来源。

## 不包含

删除类工具、方案编辑工具、OAuth、数据库迁移、独立的 MCP 界面。这些要么有风险，要么没有需求。
