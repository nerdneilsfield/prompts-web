# Prompt 库与优化工作台

私人使用的 Prompt 库：保存提示词与其标签，维护优化方案，在工作台里对照优化结果并另存。
后端 Go（标准库 HTTP + SQLite），前端 React/TypeScript/Vite，生产为单二进制，前端资源内嵌。

- 优化请求同步执行：一次 HTTP 请求一次模型调用，无后台任务、无重试、无轮询；客户端断线即取消。
- 服务端只接受环境配置的单密码；模型密钥留在后端，不写日志、不出现在响应里。
- 所有真实 Prompt、方案与运行记录都在 SQLite 中，通过 API 读写；没有任何数据存在配置文件里。

| 文档 | 内容 |
|---|---|
| `docs/api.md` | HTTP 契约（请求/响应结构、错误码、限制） |
| `docs/plans/implementation.md` | 实施记录与验收映射 |
| `design.md` | 界面设计记录与设计令牌来源 |

## 快速开始（本机，非容器）

需要 Go 1.26+ 与 Bun 1.4.3。

```bash
bun install --frozen-lockfile     # 锁文件不一致即失败，这是期望行为
bun run build                     # 生成 web/dist；Go 用 go:embed 内嵌它
go build -o bin/server ./cmd/server

export APP_PASSWORD='你的站点密码'
export APP_ORIGIN='http://127.0.0.1:8080'   # 必须与你在浏览器里输入的地址完全一致
./bin/server                      # 默认 127.0.0.1:8080
```

打开 <http://127.0.0.1:8080>，用 `APP_PASSWORD` 登录。

`APP_ORIGIN` 必须写成你实际访问的地址：写请求的同源校验要求浏览器发出的 `Origin` 与其逐字节相等，
不一致就直接 `403 bad_origin`（默认值是 `http://localhost:8080`，所以你用 `127.0.0.1:8080` 打开却只设密码时，
登录会被拒）。两者选一个保持一致即可：访问 `127.0.0.1:8080` 就设 `APP_ORIGIN=http://127.0.0.1:8080`，
访问 `localhost:8080` 就保持默认值。没有配置模型时站点照常可用：登录、库、方案、
已运行记录的查询都正常，只有新的优化请求返回 `503 model_not_configured`。

`go build` 之前必须先 `bun run build`：`web/dist` 不存在时 `go:embed` 会直接编译失败，而不是留下一个空页面。

## 配置

全部配置来自环境变量，见 `.env.example`。服务端只读这些变量，没有别的开关或配置文件。

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `APP_PASSWORD` | 是 | — | 全站单密码。缺失时服务拒绝启动。 |
| `APP_ORIGIN` | 否 | `http://localhost:8080` | 浏览器访问本站的地址。写请求的同源校验以它为准；scheme 为 `https` 时会话 Cookie 自动带 `Secure`。 |
| `DATA_DIR` | 否 | `./data` | SQLite 所在目录，服务启动时创建，文件固定为 `prompts.db`。目录权限按 `0700` 创建。 |
| `LISTEN_ADDR` | 否 | `127.0.0.1:8080` | 监听地址。本机默认只听回环；容器内必须显式设为 `0.0.0.0:8080` 才可被外部访问。 |
| `TRUST_PROXY` | 否 | `false` | 是否读 `X-Real-IP` 作为访客地址。**仅在应用端口不对外发布、只能经可信反代访问时才可设为 `true`**。详见下节。 |
| `LLM_BASE_URL` | 否 | 空 | OpenAI 兼容基址，如 `https://api.example.com/v1`，实际请求 `{LLM_BASE_URL}/chat/completions`。 |
| `LLM_API_KEY` | 否 | 空 | 上游密钥，作为 `Authorization: Bearer` 发送。 |
| `LLM_MODEL` | 否 | 空 | 上游模型 id，记录在每条运行中。 |

`LLM_BASE_URL`/`LLM_API_KEY`/`LLM_MODEL` 三者必须齐全才算配置了模型；缺任意一个即视为未配置，
服务仍会启动，只是新优化请求返回 503。采样参数固定在代码里（`temperature=0.2`、`top_p=1`、
`max_tokens=2048`），随每次运行记录在 `params` 中，不是环境变量。

`APP_PASSWORD` 以任何方式泄露都等于泄露整个库；`.env` 建议 `chmod 600`，并且它已被 `.gitignore` 忽略。

### 反向代理与 `TRUST_PROXY`

登录限速按访客地址计额度。默认（`TRUST_PROXY=false`）只认 TCP 对端地址，**完全忽略** `X-Real-IP`、
`X-Forwarded-For`、`Forwarded`。直接暴露时这是唯一安全的做法：这些头是客户端可伪造的。

但经反代部署后，所有访客的对端地址都是代理本身，于是他们共用一份额度——一个人错 8 次密码，
就把其他所有人锁在 429 之外（`Retry-After` 600 秒）。启用 `TRUST_PROXY=true` 解决这个问题，
代价是引入一个必须严守的前提：

> **前提（开启时）：应用端口只能经可信反代访问，不得对外发布。**

反代必须在每个请求上用**连接对端 IP 覆盖**（而不是追加）`X-Real-IP`；后端只读这一个头，
且只在其为单个合法 IP 时采纳。`X-Forwarded-For` 一律不读——它天生是列表，可被客户端前置伪造。

违反前提的后果是限速失效：任何能直连应用端口的人都可以自带 `X-Real-IP: 1.2.3.4`，每次尝试换一个 IP，
于是每次都是新额度。因此：

- **不要**在 `compose.yaml` 的 `app` 服务上加 `ports:`；仓库里的 Compose 正是因此把它设为 `true`。
- 若你把应用端口发布到宿主机（或让容器内其他服务可达），必须保持 `TRUST_PROXY=false`；
  那种拓扑下限速只能按代理地址计数，请改用反代自身的限速能力。
- 开启后若 `X-Real-IP` 缺失或非法，登录会被**直接拒绝**（400 `untrusted_client`），
  而不是回退到代理地址——回退会把所有访客并进同一份额度，正是要避免的失败模式。

除了限速地址，服务不依赖任何转发头：`APP_ORIGIN` 决定同源校验与 Cookie 的 `Secure`。

## 开发

前后端分开跑时，Vite 需要把 `/api` 代理到 Go 服务，并改写请求的 `Origin`——浏览器发出的
`Origin` 是 Vite 页面地址，Go 的同源校验会拒绝写请求。代理已配好，无需放宽 `APP_ORIGIN`：

```bash
APP_PASSWORD=dev-password DATA_DIR=./data go run ./cmd/server   # 终端 1：API 在 127.0.0.1:8080
bun run dev                                                     # 终端 2：页面在 http://localhost:5173
bun run typecheck                                               # tsc --noEmit
```

后端指向别的地址时用 `BACKEND_ORIGIN=http://127.0.0.1:9090 bun run dev`。

## 测试

```bash
go test ./...            # 后端：51 个测试（含子测试）
go vet ./...
bun run typecheck
bun run build
bun run test:e2e         # Playwright，14 个用例
```

Playwright 不需要你准备任何东西：`playwright.config.ts` 会自己起一个临时数据目录的 Go 服务和一个
模拟模型（`tests/mock-llm.mjs`，端口 18080/18081），用完即弃，不碰 `./data`。浏览器已缓存在本机，
首次运行若提示缺浏览器则执行 `bunx playwright install chromium`。

## 用 Apple container 运行

本机使用 Apple 自己的容器工具（`container` CLI 1.0），不需要 Docker daemon。前置条件：

```bash
container system start        # 首次使用；之后 system status 应显示 running
container builder status      # 构建需要 builder 在跑；没有就 container builder start
```

构建（在仓库根目录执行；`-f Dockerfile` 省略也可以）：

```bash
container build -f Dockerfile -t prompts-web:local .
```

镜像分三段：`oven/bun:1.4.3-alpine` 里 `bun install --frozen-lockfile` 并 `bun run build` 出 `web/dist`；
`golang:1.26-alpine` 里 `go build` 并把 `web/dist` 内嵌；运行时是 `alpine:3.22`，只有静态二进制和
CA 证书，不需要 Bun、Node 或 Go。`.dockerignore` 排除了 `.git`、`.cw`、`.env`、`data`、`node_modules`、
`bin`、`test-results`，所以这些内容不会进入镜像。

运行（测试用法：独立数据目录、非默认端口、外加一个假模型）：

```bash
mkdir -p /tmp/prompts-web-data            # 只改自己新建的临时目录

container run -d --name prompts-web-test -p 19080:8080 \
  -e APP_PASSWORD='local-test-password' \
  -e APP_ORIGIN=http://127.0.0.1:19080 \
  -e LISTEN_ADDR=0.0.0.0:8080 \
  -e DATA_DIR=/data \
  -v /tmp/prompts-web-data:/data \
  prompts-web:local
```

`LISTEN_ADDR=0.0.0.0:8080` 是容器里必须显式给出的：镜像默认值是 `127.0.0.1:8080`，那样端口映射进去也连不上。
验证与收尾：

```bash
curl -sS http://127.0.0.1:19080/api/session        # {"authenticated":false,...}
container logs prompts-web-test
container stop prompts-web-test && container rm prompts-web-test
```

数据目录权限。Apple container 把宿主 bind mount 映射为发起调用的宿主用户，因此直接用 `-v /tmp/prompts-web-data:/data`
即可，镜像里的 `/data`（uid 10001）只影响**命名卷**。实测：

- 宿主目录 `0700` 或 `0755` + bind mount：正常启动，自动建出 `prompts.db`（宿主属主）。
- 新命名卷挂到 `/data`：目录属于 root，服务以 `unable to open database file (14)` 退出；
  先一次性 `chown` 即可。

如果你想固定成镜像里的用户，显式给出 `--uid 10001`（或你自己的宿主 uid）也照样可用；
不需要为此放宽目录权限：

```bash
# 方式一：宿主 bind mount（推荐，数据文件属宿主用户，备份最直接）
mkdir -p /tmp/prompts-web-data
container run -v /tmp/prompts-web-data:/data ...

# 方式二：命名卷，先一次性把卷目录交给 10001
container volume create prompts-web-data
container run --rm --uid 0 -v prompts-web-data:/data --entrypoint sh prompts-web:local -c 'chown 10001:10001 /data'
container run -v prompts-web-data:/data ...
```

服务不会去修改挂载目录的属主，也不要为了省事把数据目录整个 `chmod 777`。

## 可选：Compose + HTTPS 反向代理

只在你有真实域名和服务器时使用。本机用上面的 Apple container 方式就够了。**没有真实凭据不要上线。**

```bash
cp .env.example .env                 # 填 APP_PASSWORD、APP_ORIGIN、域名与模型配置；chmod 600 .env
# 编辑 deploy/Caddyfile，把 {$APP_DOMAIN} 对应的域名换成你的真实域名
docker compose up -d --build
```

`compose.yaml` 只有一个应用实例加一个 Caddy 容器。应用不映射任何宿主端口，只有 Caddy 能访问它，
TLS 全部在 Caddy 终止；`APP_ORIGIN` 必须是真实的 `https://` 地址，服务据此自动给会话 Cookie 加 `Secure`。
Caddy 会自动申请 Let's Encrypt 证书，前提是域名已解析到该主机且 80/443 可达。`deploy/Caddyfile` 里显式
放宽了代理超时，因为一次优化同步等待最长 90 秒，任何更短的代理超时都会把它切断。

Caddy 还会用连接对端 IP 覆盖 `X-Real-IP`，并由 `compose.yaml` 设 `TRUST_PROXY=true`，使登录限速按
访客而非代理计数。这一步成立的前提是**应用端口不对外发布**——仓库里确实如此。如果给 `app` 服务
加了 `ports:`，请同时把 `TRUST_PROXY` 改回 `false`，否则限速可被伪造头绕过（见上节）。

站点是单实例设计：SQLite 单连接、会话在内存。**不要把它横向扩成多个副本**，会话不共享，数据目录也会互相冲突。

## 演示数据

`fixtures/demo.json` 有 6 条 Prompt 和 3 个方案，全部带 `演示` 标签，**不会随服务启动自动导入**，必须手动执行：

```bash
bun scripts/import-demo.mjs --url http://127.0.0.1:8080 --password "$APP_PASSWORD"
```

脚本通过公开的登录与 CRUD 接口写入，数据落进 SQLite，和手工添加的条目没有区别（只是带 `演示` 标签）。
以标题为准判重，重复执行只会提示「已存在」，不会产生副本。脚本只接受 `--url`/`--password`（或环境变量
`APP_URL`/`APP_PASSWORD`），不记录密码，也不创建任何优化运行——演示条目不是优化效果的证据。

导入后可用标签筛选或关键词搜索查看，例如 `GET /api/prompts?tag=演示`。

## 评测脚本

`scripts/eval.mjs` 回答一个问题：**同一个模型、同一个任务输入，用优化后的 Prompt 是否比用原始
Prompt 得到更好的任务结果。** 模型执行的是任务本身（写纪要、审代码、改邮件、抽 JSON），不是改写 Prompt。

它只调用 OpenAI 兼容的 `/chat/completions`，不登录本站、不创建方案、不调用 `/api/optimizations`、
不与 Prompt 库关联。站点优化器可以是另一个模型：你可以在工作台里先做好优化，再把结果手写或导出到样本的
`optimized` 字段。

```bash
export EVAL_BASE_URL=https://api.example.com/v1   # 三项缺一即报清晰的配置错误并退出
export EVAL_API_KEY=sk-...
export EVAL_MODEL=some-model

bun scripts/eval.mjs --samples fixtures/eval-samples.jsonl --out eval-out
```

样本是 JSONL，每行一个用例：

```json
{"id":"json-extract","input":"张伟，销售部，工号 1024，入职 2023-05-06。",
 "original":"把下面这段信息整理成 JSON：\n{{输入}}",
 "optimized":"把下面文本中的字段抽取为 JSON 对象……\n{{输入}}",
 "checks":{"kind":"json_object","required_keys":["name","department","employee_id","hire_date"],"max_chars":4000},
 "rubric":"键名是否完全一致；日期格式是否为 YYYY-MM-DD；是否有多余文字。"}
```

- `{{输入}}` 是任务输入的代入点；模板里没有它时，输入以 `--- 待处理内容 ---` 追加在末尾。
- 每个输入每臂跑 **3 次**，共 **6 次调用**；两臂使用完全相同的 system 消息、任务输入与采样参数
  （`temperature=0.2`、`top_p=1`、`max_tokens=2048`），唯一变量是待测 Prompt 文本。
- 可执行检查只做少量直接机制：合法 JSON、必需键、必须出现的子串、最大 Unicode 字符数。
  没有 checks 的用例不自动判好坏，交给人工 A/B。
- **不按长度打分，也不让模型给自己打分。**

输出全部写在 `--out` 目录（默认 `eval-out/`，已被 git 忽略）：

| 文件 | 内容 |
|---|---|
| `responses.jsonl` | 每次调用一行：样本、臂、序号、模型、实际参数、状态、错误、耗时、时间、完整 system/prompt/输出 |
| `blind-items.jsonl` | 随机 A/B 盲评材料：只有 `item_id`、任务输入、a/b 两段输出、rubric。不含臂名、检查结果、运行序号 |
| `blind-map.json` | 单独存放的映射：每个 item 的 `optimized_side` 与来源样本、运行序号 |
| `blind-annotations.template.json` | 待填的投票模板 |
| `summary.json` | 通过数、失败计数、逐样本计数、退化样本、盲评统计 |

盲评投票与汇总：

```bash
cp eval-out/blind-annotations.template.json eval-out/blind-annotations.json
# 逐项填 "a" / "b" / "tie"；留 null 表示不评，会计入未评分
bun scripts/eval.mjs --summarize eval-out --annotations eval-out/blind-annotations.json
```

`--summarize` 是离线的：只读 `responses.jsonl` 与 `blind-map.json`，**不会再调用模型**，因此反复汇总不产生费用。
它会给出优化胜/原始胜/平票数、未评分条数，以及**退化样本**（优化臂通过检查的轮次少于原始臂）——
退化帧列进 `degenerate_samples`，不会被平均值掩盖。

想先用假模型验证管线（不需要任何密钥、不产生费用）：

```bash
bun scripts/mock-model.mjs &                       # 127.0.0.1:19100 上一个固定的假模型
EVAL_BASE_URL=http://127.0.0.1:19100/v1 EVAL_API_KEY=mock EVAL_MODEL=mock-model \
  bun scripts/eval.mjs --samples fixtures/eval-samples.jsonl --out /tmp/eval-out
```

假模型对两臂给出同样的答案，所以检查结果会持平——这正是重点：**假模型只能证明脚本能跑通，
不能证明优化有效。** 用真实模型跑出来的数字同样受样本量小、任务单一的限制；3 次重复只能说明波动，
不能当作统计结论。

## 备份、恢复与升级

要备份的东西只有一个：`DATA_DIR` 整个目录（里面就是 `prompts.db`）。会话在内存里，没有别的状态。**必须先停服务**，
运行中直接拷贝可能拿到写了一半的数据库文件。

用 bind mount 时，数据本来就是宿主上的普通目录，用宿主命令复制即可：

```bash
# 假设启动时用的是 -v /tmp/prompts-web-data:/data
container stop prompts-web-test          # compose 部署：docker compose stop app
tar czf /tmp/prompts-web-backup-$(date +%F).tar.gz -C /tmp/prompts-web-data .
mkdir -p /tmp/prompts-web-data-restored
tar xzf /tmp/prompts-web-backup-$(date +%F).tar.gz -C /tmp/prompts-web-data-restored
# 恢复：改用 -v /tmp/prompts-web-data-restored:/data 启动，或把解出的内容放回原目录后再启动
container start prompts-web-test
```

用**命名卷**时宿主上看不到普通文件，要靠容器只读导出。下面这套命令在 Apple container 1.0 上完整验证过：
备份、恢复到**另一个专用卷**、启动后数据（含 Prompt 条目）都在。

```bash
container stop prompts-web-test

# 1) 备份：只读挂载数据卷，导出 tar 到宿主目录（--entrypoint sh 不能省）
mkdir -p /tmp/prompts-web-backups
container run --rm --uid 0 \
  -v prompts-web-data:/data:ro \
  -v /tmp/prompts-web-backups:/backup \
  --entrypoint sh prompts-web:local \
  -c 'tar czf /backup/prompts-web-backup.tar.gz -C /data .'

# 2) 恢复：新建一个专用卷，先给它一次属主，再解包进去
container volume create prompts-web-data-restored
container run --rm --uid 0 -v prompts-web-data-restored:/data \
  --entrypoint sh prompts-web:local -c 'chown 10001:10001 /data'
container run --rm --uid 0 \
  -v prompts-web-data-restored:/data \
  -v /tmp/prompts-web-backups:/backup \
  --entrypoint sh prompts-web:local \
  -c 'tar xzf /backup/prompts-web-backup.tar.gz -C /data'

# 3) 用恢复出来的卷启动并确认
container run -d --name prompts-web-restored -p 19080:8080 \
  -e APP_PASSWORD='local-test-password' -e APP_ORIGIN=http://127.0.0.1:19080 \
  -v prompts-web-data-restored:/data prompts-web:local
curl -sS http://127.0.0.1:19080/api/session     # 用原密码登录后能看到原有 Prompt
```

几点说明：导出用 `--uid 0` 只是为了让 `tar` 能读卷里的全部文件并避开 `lost+found` 的权限告警，
它**不会修改卷里任何数据的属主**；`--entrypoint sh` 必须写，否则镜像的 ENTRYPOINT 会把 `-c …` 当成服务参数。
上面只操作你自己新建的专用卷，不要对正在使用的卷做解包。也确实存在 `tar: ./lost+found: Permission denied`
这个告警（不加 `--uid 0` 时），它只影响该空目录，不影响 `prompts.db`，但退出码会非 0，所以按上面的写法来。

用 **Compose 部署**时，数据卷不是宿主上的普通目录，也不是你随手写的名字：`compose.yaml` 声明的是
`app-data`，实际卷名带项目名前缀（本项目目录名即项目名，所以默认是 `prompts-web_app-data`）。
Name 会被 `COMPOSE_PROJECT_NAME` 或 `-p` 改变，因此不要凭记忆拼，先用 Compose 自己报出来：

```bash
docker compose config --format json | python3 -c 'import sys,json;print(json.load(sys.stdin)["volumes"]["app-data"]["name"])'
# 或从正在跑的容器读（更直接，顺带确认挂载点）
docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$(docker compose ps -aq app)"
```

备份（先停 app；`--no-deps` 避免顺带起 Caddy，`--entrypoint sh` 覆盖掉镜像的 Go ENTRYPOINT，
`--user 0:0` 只是为了 `tar` 读得到卷里全部文件，不修改任何属主）：

```bash
cd /path/to/prompts-web
docker compose stop app
mkdir -p "$PWD/backups"
docker compose run --rm --no-deps --user 0:0 \
  -v "$PWD/backups:/backup" --entrypoint sh app \
  -c 'tar czf /backup/prompts-web-backup.tar.gz -C /data .'
ls -l backups/prompts-web-backup.tar.gz
```

其中 `-v "$PWD/backups:/backup"` 不能省：service 定义里只挂了 `/data`，没有宿主目录，
一次性容器里默认看不到你的 `backups/`，而没有 `/backup` 就没有地方写 tar。
反过来，**不需要**再写 `-v prompts-web_app-data:/data`：service 已声明 `/data` 挂载，
一次性容器用的就是同一个卷，重复写反而容易写错卷名。

恢复到**另一个专用卷**，不让恢复过程碰到正在用的那个卷：

```bash
docker volume create prompts-web_app-data-restored
# 新卷是 root 属主，而镜像 USER 是 10001，所以两条命令都必须 --user 0:0：
# 没有它，chown 自己就没权限，解包也无法还原 tar 里 10001:10001 的属主。
docker run --rm --user 0:0 -v prompts-web_app-data-restored:/data --entrypoint sh prompts-web:local \
  -c 'chown 10001:10001 /data'
docker run --rm --user 0:0 -v prompts-web_app-data-restored:/data -v "$PWD/backups:/backup" \
  --entrypoint sh prompts-web:local -c 'tar xzf /backup/prompts-web-backup.tar.gz -C /data'
```

切换 Compose 使用恢复出来的卷：不用改 `compose.yaml`，加一个最小 override 把 `app-data` 指向真实卷名即可
（service 定义完全不动）：

```yaml
# compose.restore.yaml —— 恢复后长期保留，不能删
volumes:
  app-data:
    name: prompts-web_app-data-restored
```

```bash
docker compose -f compose.yaml -f compose.restore.yaml up -d app
docker compose logs --tail 20 app       # 确认监听正常，再用原密码登录验证数据
```

**这个 override 要一直带着**：卷名只存在于 override 里，不带它就回到主配置的 `app-data`，
默认 `up` 会直接切回原卷（数据看起来“回退了”）。所以后续**所有**命令都要带同样的 `-f`，
包括 `config`、`stop`、`ps`、以及升级时的 `up`：

```bash
docker compose -f compose.yaml -f compose.restore.yaml config
docker compose -f compose.yaml -f compose.restore.yaml stop app
docker compose -f compose.yaml -f compose.restore.yaml up -d --build
```

想不带 `-f`（或把它交给 systemd/其他运维脚本时），就把恢复卷名写进主配置：把 `compose.yaml` 末的
`app-data:` 改成 `app-data: { name: prompts-web_app-data-restored }`，确认 `docker compose config` 后
再删掉 override。两条路选一条，不要既删 override 又不改主配置。

override 的合并规则值得记一句，因为它决定了为什么上面改的是卷名而不是 mount 列表：Compose 按**容器内挂载点**合并
volume 条目，所以 `app-data-restored:/data` 会替换原来那条 `/data`（不是叠加），而改成 `:/data2` 才会变成两个挂载。
两者都实测过：同 target 得到 1 个 `/data` 挂载，异 target 得到 `/data` + `/data2` 两个。
换句话说挂 mount 列表也能用，只是要确保 target 写成 `/data`；改卷名更短且不依赖这个细节。
切换后 `docker compose config` 确认 `/data` 只有一个挂载，且 `volumes.app-data.name` 指向恢复出来的卷。
原卷 `prompts-web_app-data` 不会被这个流程删除（不含 `down -v` 或 `volume rm`）。

**本机（macOS，无 Docker daemon）没有实际跑过上面这些 Compose 命令**，只做了 `docker compose config`
的静态解析。静态解析确认的事实：卷名确实解析为 `prompts-web_app-data`（前缀跟随项目名）；上面的恢复 override
解析后只剩一个 `/data` 挂载且指向 `prompts-web_app-data-restored`；同名挂载点会替换、换挂载点才会追加。
未验证的部分：`docker compose run` 一次性容器与上面两条 `docker run` 的实际执行（本机无 daemon）。
`--user 0:0` 的必要性与“Docker 会不会用镜像目录初始化新命名卷”无关：卷是 root 属主而镜像 USER 是 10001，
这点已在 Apple container 上实测（不加则 chown 与解包都因权限失败），所以这里不依赖 Docker 的行为差异。

**恢复会覆盖目标位置**：解包前确认那个卷/目录里没有你还需要的 `prompts.db`，这个操作不可撤销。备份包含你的全部
Prompt 和方案，按敏感数据处理。

升级：先按上面做备份，再换新镜像/新二进制，最后启动并 `curl /api/session` 确认能登录。Go 二进制之间
没有版本回滚封装，**回退就是把旧二进制放回去**，并配套使用相应时间点的数据备份——代码与数据的兼容
边界只有一条：新版本启动时会把库中遗留的 `running` 运行标为 `interrupted`，并自动建出缺失的表。
除此外没有迁移步骤，也不会主动删除数据。如果新版本改动过表结构而你要回退，请连同备份一起回退。

## 目录结构

```
cmd/server/        入口：读环境变量、打开 SQLite、内嵌前端并监听
internal/app/      API、会话、SQLite 存储、模型调用（全部业务逻辑）
web/               React 前端；web/embed.go 用 go:embed 内嵌 web/dist
web/dist/          前端构建产物，由 bun run build 生成，不提交
tests/             Playwright 用例与模拟模型
fixtures/          演示数据与评测样本，仅手动使用
scripts/           主动导入与评测脚本（不参与镜像构建，见 .dockerignore）
deploy/Caddyfile   可选 Compose 部署用的反向代理配置
Dockerfile         三段式镜像：Bun 构建前端 → Go 内嵌编译 → alpine 运行时
```

## 已知限制

- 单实例、单密码、无多用户与权限区分；任何登录者都能读写全库。
- 没有后台任务、版本历史或站内评测平台；运行记录只保留当次执行的方案快照。
- 会话在内存，重启即失效，表现为前端一直 401，重新登录即可。
- 优化请求无重试：上游失败即失败，输入由前端保留。
- 镜像不含 `.git`、`.cw`、`.env` 或任何真实数据；Apple container 上的命名卷需要一次性 `chown`（见上）。
