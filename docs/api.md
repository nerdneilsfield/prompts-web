# Prompt 网站后端 API

任务 1（后端）交付的 HTTP 契约。前端按此对接，不需要再读 Go 代码。

服务由 Go 标准库 `net/http` 实现，状态在 SQLite。**没有流式、没有轮询、没有后台任务**：一次优化就是一个同步 HTTP 请求，客户端断线即取消。

## 挂载与入口

```go
func NewServer(store *Store, cfg Config, assets fs.FS) (http.Handler, error)
```

- `store` 由 `OpenStore(path)` 得到，进程内单例。
- `assets` 可以是 `nil`（纯 API，测试用）。非 nil 时挂载 `GET /` 提供前端静态资源；`/api/*` 始终优先。
- 入口不启动任何后台 goroutine。会话与登录限速表在相关请求中顺带清理。

```go
cfg := app.DefaultConfig()
cfg.Password = ...        // 必填
cfg.DBPath = ...          // 必填
store, err := app.OpenStore(cfg.DBPath)
handler, err := app.NewServer(store, cfg, nil)
http.ListenAndServe(":8080", handler)
```

## Config

`app.Config` 字段即部署配置。主程序从环境变量读取后填入，后端不读环境变量。

| 字段 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `Password` | `string` | 无，必填 | 全站单密码。仅后端持有。 |
| `DBPath` | `string` | 无，必填 | SQLite 文件路径，持久化目录。 |
| `CookieSecure` | `bool` | `false` | 是否给会话 Cookie 加上 `Secure` 属性（仅 HTTPS 才回传）。**生产必须 `true`**（HTTPS 反代）；本地明文 HTTP 调试用 `false`。注意它只控制这一个属性：`false` 时 Cookie 照样下发，明文 HTTP 下浏览器也会发送它，区别仅是 HTTP 页面下不会被拒。任务 2 的主程序应据 `APP_ORIGIN` 自动推导该值，不新增环境变量旋钮。 |
| `SessionTTL` | `time.Duration` | `24h` | 会话有效期。会话本身在内存中，重启即失效。 |
| `AllowedOrigins` | `[]string` | 空 | 写请求允许的精确来源，形如 `https://prompts.example.com`。空表示只接受无 `Origin` 的请求（CLI）。每项必须以 `http://` 或 `https://` 开头。 |
| `LoginMaxFailures` | `int` | `8` | 单个客户端 IP 在窗口内允许的**登录尝试**次数（成功与失败都计入，成功会清零），超出返回 429。额度在读请求体之前原子预占，因此并发尝试不能靠时间差绕过。 |
| `LoginWindow` | `time.Duration` | `10m` | 失败计数的滑动窗口。 |
| `RequestTimeout` | `time.Duration` | `90s` | 单次上游模型调用上限。生产保持 90 秒；测试可设更短。 |
| `BaseURL` | `string` | 空 | OpenAI 兼容基址，如 `https://api.example.com/v1`。实际请求 `{BaseURL}/chat/completions`。`BaseURL`/`APIKey`/`Model` 三者全有才算已配置，缺任一即未配置（不阻止启动）。 |
| `APIKey` | `string` | 空 | 上游密钥，作为 `Authorization: Bearer`。不写日志、不出现在任何响应。 |
| `Model` | `string` | 空 | 上游模型 id，写入每条运行记录。 |
| `Temperature` | `float64` | `0.2` | 实际发送并记录在 `params`。 |
| `TopP` | `float64` | `1` | 同上。 |
| `MaxTokens` | `int` | `2048` | 同上。 |
| `MaxBodyBytes` | `int64` | `0`（即 1 MiB） | 单次请求体上限，超出返回 413。 |

`NewServer` 只对真正无法服务的配置返回错误：缺 `Password`、缺 `DBPath`、非正的 `SessionTTL`/`RequestTimeout`/`LoginMaxFailures`/`LoginWindow`、格式非法的 `AllowedOrigins`、负的 `MaxBodyBytes`，以及 `store` 为 nil。**模型配置不是启动前提**：缺 `BaseURL`/`APIKey`/`Model` 时服务照常启动，登录、Prompt 库、方案与已有运行的查询全部可用，仅新的优化请求返回 503（见下）。

## 通用约定

- 请求与响应都是 JSON，`Content-Type: application/json`。
- **列表接口直接返回 JSON 数组**，只有写操作的最小信封，例如 `GET /api/prompts` → `[{...},{...}]`，空列表是 `[]` 而不是 `null`。
- **错误统一为**：

  ```json
  {"error":{"code":"bad_request","message":"title must not be empty"}}
  ```

  `message` 是固定的英文短语，不含上游正文、密钥或原始错误；`code` 是稳定的机器可读值（见文末错误码表）。
- 时间戳为 UTC RFC3339，纳秒精度，固定宽度，可直接字符串排序比较。
- 未知路径返回 404（标准库文本响应，非 JSON 信封）；已注册路径用错方法返回 405。

### 鉴权

除 `GET /api/session` 与 `POST /api/session`（登录本身）外，所有 `/api/*` 都需要会话 Cookie，否则 401 `unauthorized`。登录成功后服务端下发：

```
Set-Cookie: session=<32 字节随机 hex>; Path=/; Max-Age=86400; HttpOnly; SameSite=Strict[; Secure]
```

`Secure` 由 `CookieSecure` 决定，它只控制这一个属性。`CookieSecure=false` 时 Cookie 正常下发，明文 HTTP 与 HTTPS 下浏览器都会发送；差别仅是缺少 `Secure`，因而在 HTTP 页面上不会被拒绝。会话保存在内存，**进程重启后全部失效**（表现为一直 401，前端应引导重新登录）。客户端需带 Cookie 发请求（同源 fetch 加 `credentials: "same-origin"` 即可，`credentials: "include"` 也无妨）。

### 同源校验

写操作（`POST` / `PUT` / `DELETE`）校验 `Origin` 头：

- 无 `Origin`：放行。CLI、评测脚本属于此类。浏览器一定会带上 `Origin`，所以这不削弱防护。
- 有 `Origin` 且与 `AllowedOrigins` **逐字节完全相等**：放行。
- 有 `Origin` 且不匹配：403 `bad_origin`。`https://evil-prompts.example.com` 不会因前缀相同而通过。

`GET` 不校验来源。

## 会话

### `GET /api/session`

无需登录。用于启动时判断是否显示登录页，以及是否禁用“优化”入口。

```json
{"authenticated": false, "model_configured": true}
```

- `authenticated`：Cookie 是否有效。
- `model_configured`：后端是否已配置完整的上游模型。为 `false` 时前端应禁用/隐藏“载入优化”，而不是等用户点下去才报错（登录后也应看这个字段，因为它与登录状态无关）。

两种状态都返回 `200`；无 Cookie、Cookie 未知或已过期时 `authenticated` 为 `false`。

### `POST /api/session`

登录。需要同源校验。

请求：`{"password":"..."}`

- `200 {"authenticated": true, "model_configured": true}` + `Set-Cookie`。成功登录的响应带与 `GET /api/session` 相同的派生 `model_configured`，前端登录后无需再查一次就能决定是否提供优化入口。
- `401 {"error":{"code":"unauthorized",...}}` — 密码错误。
- `429 {"error":{"code":"rate_limited",...}}` + `Retry-After`（秒）— 该 IP 的尝试额度已用尽，**此时正确密码同样被拒**，直到窗口滑过（成功登录会清零计数）。额度在读取请求体和比对密码之前原子预占，因此并发尝试不能超出上限；代价是超限时连格式非法的请求体也返回 429。
- `400 bad_request` — 空请求体或非法 JSON。

### `DELETE /api/session`

退出登录，需要鉴权与同源校验。清除服务端会话并下发过期 Cookie。

- `200 {"authenticated": false}`

## Prompt 库

Prompt 对象：

```json
{
  "id": 1,
  "title": "总结文章",
  "body": "请总结下面这篇文章",
  "tags": ["总结", "写作"],
  "source_run_id": "0f8f...",
  "created_at": "2026-02-14T03:11:07.123456789Z",
  "updated_at": "2026-02-14T03:11:07.123456789Z"
}
```

`source_run_id` 在没有来源运行时为 `null`。正文是纯文本，不做变量填写。

### `GET /api/prompts?q=&tag=`

鉴权。返回数组，按 `updated_at` 倒序（新的在前）。

- `q`：子串匹配，作用于 `title` 或 `body`，服务端过滤。大小写不敏感仅限 ASCII（SQLite `lower()` 的范围）；中文等非 ASCII 按精确子串匹配。
- `tag`：精确匹配单个标签，区分大小写。
- 两者可同时给出，取交集。参数超长返回 400。

### `POST /api/prompts`

鉴权 + 同源。请求：

```json
{"title":"总结文章","body":"请总结下面这篇文章","tags":["总结"],"source_run_id":"0f8f..."}
```

- `tags` 可省略；发送时逐项去除首尾空白、丢弃空项与重复项，保持输入顺序。
- `source_run_id` 可省略或为 `null`。给出时必须指向一个已 `succeeded` 的运行。
- `201` + 完整 Prompt 对象。
- `400 bad_request` — 标题为空、正文为空或全空白、字段超长、标签超过 32 个或单个超 64 字符、非法 JSON、空请求体、请求体含多于一个 JSON 值、超过 `MaxBodyBytes`（413）。
- `409 bad_source_run` — `source_run_id` 不存在，或对应运行不是 `succeeded`（失败、取消、中断的运行都不算合法出处）。

### `GET /api/prompts/{id}`

鉴权。`200` + Prompt 对象；`404 not_found`；`400 bad_request`（id 非正整数）。

### `PUT /api/prompts/{id}`

鉴权 + 同源。请求体同创建，但 `tags` 整体替换。**`source_run_id` 不参与更新**，已记录的出处不会被改写。`created_at` 保持不变，`updated_at` 刷新。

- `200` + 更新后的 Prompt 对象；`404 not_found`。

### `DELETE /api/prompts/{id}`

鉴权 + 同源。`204`（无响应体）；`404 not_found`。

## 方案

Scheme 对象：

```json
{"id":1,"name":"精简","body":"去掉冗余表达","created_at":"...","updated_at":"..."}
```

### `GET /api/schemes`

鉴权。返回数组，按 `updated_at` 倒序。工作台的下拉列表用它。

### `POST /api/schemes`

鉴权 + 同源。请求 `{"name":"精简","body":"去掉冗余表达"}`。

- `201` + Scheme 对象。
- `400 bad_request` — 名称或正文为空，或超长。

### `GET /api/schemes/{id}`

鉴权。`200`；`404`；`400`（id 非正整数）。

### `PUT /api/schemes/{id}`

鉴权 + 同源。`200` + Scheme 对象；`404`。

方案改名或改正文**不会**影响任何已有运行记录：运行里存的是执行当时的快照。

### `DELETE /api/schemes/{id}`

鉴权 + 同源。`204`；`404`。

删除方案后，引用它的运行记录依然可以按 `request_id` 查询与重放。

## 优化运行

Run 对象：

```json
{
  "request_id": "0f8f1c1e-...",
  "input": "原文",
  "scheme_id": 1,
  "scheme_name": "精简",
  "scheme_snapshot": "去掉冗余表达",
  "model": "gpt-x",
  "params": {"temperature":0.2,"top_p":1,"max_tokens":2048,"stream":false},
  "result": "优化后的 prompt",
  "status": "succeeded",
  "created_at": "2026-02-14T03:11:07.123456789Z",
  "finished_at": "2026-02-14T03:11:12.987654321Z"
}
```

- `status`：`running` / `succeeded` / `failed` / `interrupted`。
- `error_kind` 仅在 `status` 为 `failed` 或 `interrupted` 时出现：`auth` / `rate_limit` / `upstream` / `invalid` / `timeout` / `canceled` / `interrupted`。
- `result` 是模型返回的原始文本，**编辑后的正文只进 Prompt 库**，不回写运行记录。失败时为空字符串。
- `params` 是实际发送的采样参数，不含消息文本（原文与方案快照各有独立字段）。
- `scheme_id` 是提交时的方案选择；`scheme_name` / `scheme_snapshot` 是执行当时的快照。此后改动或删除方案，这三个字段都不变。
- `finished_at` 在 `status` 为 `running` 时缺省。

### `POST /api/optimizations`

鉴权 + 同源。同步执行：请求挂住直到模型返回、失败或超时（生产 90 秒）。

请求：

```json
{"request_id":"0f8f1c1e-...","input":"原文","scheme_id":1}
```

- `request_id`：客户端生成，作为**去重键**。1–128 个字符，只允许 `[A-Za-z0-9._-]`（UUID 天然满足）。这个字符集保证它也能安全地作为 URL 路径段回来查询。
- `input`：原文，不能为空或全空白。
- `scheme_id`：必须是已存在的方案（正整数）。首版**不接受内联方案正文**，方案由 `/api/schemes` 管理。

响应是 `200` + Run 对象，**无论是成功还是模型失败**。失败通过 `status` + `error_kind` 表达：请求本身被正确受理了，是上游出错。只有**无法将终态写入数据库**时才返回 `500 internal`，因为此时后端无法诚实告知这次运行的结果。

去重语义：

- 首次使用某个 `request_id`：插入（唯一键）成功后才会调用上游。响应是本次执行的结果。
- 同一 `request_id` 再次到达，且 `input` 与 `scheme_id` 都与记录一致：**不调用上游**，直接返回已有运行的状态与结果（幂等重放）。这是丢响应后的恢复路径。
- 同一 `request_id`，但 `input` 或 `scheme_id` 与记录不同：`409 request_id_conflict`。一个 ID 不能代表两次不同的运行。
- 重放判定**只比对已记录的 `input` 与 `scheme_id`**，不读取当前方案正文。因此方案被改名或删除后，同 ID 重放依然返回原结果。

#### 并发同 ID：`202` + `status: "running"`

同一 `request_id` 的多个请求同时到达时（典型场景：用户重复点击、或超时后客户端自动重发），只有一个请求能插入成功并调用上游；其余**立刻**返回已有记录：

- 已有记录已结算（`succeeded` / `failed` / `interrupted`）→ `200` + 该 Run 对象。
- 已有记录仍 `running`（第一个请求还在调上游）→ **`202 Accepted` + `status: "running"` 的 Run 对象**。后端不等待、不阻塞、也不发起第二次上游调用。

前端必须把 `202` + `running` 当作“已有另一个请求在跑，结果稍后才有”处理，**不要重试、不要轮询**：

1. 不因 `202` 报错或自动重发（重发也只会再得到一个 `202`）。
2. 保留输入，等**当前已有的那个请求**的响应（如果它是本页面发起的，它的响应带着终态）。
3. 若当前请求是重发方向、不知道原请求结果（例如超时后发现 `202`），用 `GET /api/optimizations/{requestId}` **查一次**；若仍是 `running`，提示用户稍后手动刷新，不要定时轮询。
4. `status != "running"` 即终态，此后无需再查。

注意：`202` 由「已有记录仍为 `running`」决定，而不是“正在并发”本身。除并发同 ID 外，还有一种情况会得到 `202`：上次运行已实际调用上游但**终态写入失败**（返回了 `500 internal`），该 `running` 行留在库里，随后同 ID 重放就会命中这个分支。前端对二者的处理方式相同 —— 不重试、不轮询，用 `GET` 查一次。`input` 或 `scheme_id` 不同的重放仍是 `409`，不是 `202`。

其他响应：

- `400 bad_request` — `request_id` 缺失/超长/含非法字符、`input` 为空或超长、`scheme_id` 缺失或非正整数。
- `400 bad_scheme` — `scheme_id` 对应的方案不存在（仅在尚无该 ID 的运行记录时判定）。
- `400` / `413` — 请求体非法 JSON、空、含多个 JSON 值、或超过 `MaxBodyBytes`。
- `401 unauthorized` / `403 bad_origin` — 未登录或来源不允许。
- `503 model_not_configured` — 后端未配置完整的模型（`GET /api/session` 的 `model_configured` 为 `false`），**且这个 `request_id` 尚无运行记录**。不调用上游，不插入 `running` 行。已有记录的查询与重放不受影响，仍正常返回。
- `500 internal` — 终态无法写入数据库。此时模型**不会被重调**，运行行仍为 `running`，可用 `GET` 查询（若存储已恢复，启动扫描会将其标为 `interrupted`）。

### `GET /api/optimizations/{requestId}`

鉴权。查询丢失响应对应的运行。前端在请求超时或页面重载后用它恢复，**不轮询**：`200` 的 Run 对象一旦 `status != "running"` 就是终态，可以停止查询。

- `200` + Run 对象（含仍 `running` 的运行）。
- `404 not_found` — 无此 `request_id`。

### 失败分类

| `error_kind` | 触发条件 | 前端建议 |
|---|---|---|
| `auth` | 上游 401/403 | 提示服务端密钥失效，用户重试无意义 |
| `rate_limit` | 上游 429 | 提示稍后重试（服务端不自动重试） |
| `upstream` | 上游其他 4xx/5xx，或连接层失败 | 提示上游错误 |
| `invalid` | 2xx 但响应不是可用 JSON、无 choices、内容为空、或超过 4 MiB | 提示模型返回异常 |
| `timeout` | 超过 `RequestTimeout` | 提示超时，保留输入 |
| `canceled` | 客户端断开 | 用户自己取消的，静默 |
| `interrupted` | 进程在该运行结束前退出，启动时被标记 | 提示需重新执行 |

上游错误正文（上游响应体、HTTP 错误细节）**不会**返回给客户端，也不写日志。服务端日志同样不记录密码、`APIKey`、原文与方案正文。

## 约束与限制

| 限制 | 值 | 越界表现 |
|---|---|---|
| 请求体 | `MaxBodyBytes`（默认 1 MiB） | 413 `too_large` |
| 单个 Prompt 标题/正文、方案正文 | 200 000 字符 | 400 `bad_request` |
| 方案名称 | 200 字符 | 400 |
| 标签 | 32 个，单个 64 字符 | 400 |
| 优化原文 | 200 000 字符 | 400 |
| `request_id` | 128 字符，`[A-Za-z0-9._-]` | 400 |
| 上游响应 | 4 MiB | 运行失败，`error_kind=invalid` |
| 搜索参数 `q` / `tag` | 500 / 200 字符 | 400 |

## 错误码

| `code` | HTTP | 含义 |
|---|---|---|
| `bad_request` | 400 | 请求体非法、字段缺失或越界 |
| `bad_origin` | 403 | `Origin` 与 `AllowedOrigins` 不匹配 |
| `bad_scheme` | 400 | `scheme_id` 不存在 |
| `bad_source_run` | 409 | `source_run_id` 不存在或其运行未成功 |
| `internal` | 500 | 服务端故障，细节只在日志。优化请求遇此错误时，终态未能写入，运行仍为 `running` |
| `not_found` | 404 | 资源不存在 |
| `model_not_configured` | 503 | 后端未配置完整模型，无法执行新的优化 |
| `rate_limited` | 429 | 该 IP 的登录尝试额度已用尽 |
| `request_id_conflict` | 409 | 同一 `request_id` 被用于不同的 `input`/`scheme_id` |
| `too_large` | 413 | 请求体超过上限 |
| `unauthorized` | 401 | 未登录或密码错误 |

## 上游请求

`POST {BaseURL}/chat/completions`，非流式，`Authorization: Bearer {APIKey}`：

```json
{
  "model": "gpt-x",
  "messages": [
    {"role": "system", "content": "<固定指令：保留原意、缺失事实用占位符、不编造、只输出正文>"},
    {"role": "user", "content": "<方案正文>\n\n--- 原文 ---\n<原文>"}
  ],
  "temperature": 0.2,
  "top_p": 1,
  "max_tokens": 2048,
  "stream": false
}
```

取 `choices[0].message.content` 作为 `result`。**无重试**：一次请求一次调用，失败即失败，客户端保留输入。
