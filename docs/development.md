# 开发

本页列出开发环境、构建检查、前后端分离调试和测试命令。部署见 [`deploy.md`](deploy.md)，HTTP 接口见 [`api.md`](api.md)。

环境：Go 1.26、Bun 1.4.3。

## 目录

```
cmd/server/        入口：读环境变量，打开 SQLite，内嵌前端并监听
internal/app/      API、会话、SQLite 存储、模型调用
web/               React 前端；web/embed.go 用 go:embed 内嵌 web/dist
web/dist/          bun run build 的输出，不提交
tests/             Playwright 用例与模拟模型
fixtures/          演示数据与评测样本
scripts/           演示导入与评测脚本
deploy/Caddyfile   Compose 部署用的反向代理配置
design.md          界面设计规则与 tokens 说明
```

## 构建与检查

```bash
bun install --frozen-lockfile
bun run typecheck
bun run build
go build -o bin/server ./cmd/server
go vet ./...
```

`go build` 前必须先执行 `bun run build`。`web/dist` 不存在时 `go:embed` 报编译错误。

## 前后端分离调试

Vite 开发服务器把 `/api` 代理到 Go 服务，并把请求的 `Origin` 改写为后端地址，所以不需要修改 `APP_ORIGIN`。

```bash
APP_PASSWORD=dev APP_ORIGIN=http://127.0.0.1:8080 DATA_DIR=./data go run ./cmd/server   # 终端 1
bun run dev                                                                              # 终端 2，页面在 http://localhost:5173
```

后端在其他地址时，设置 `BACKEND_ORIGIN`，例如 `BACKEND_ORIGIN=http://127.0.0.1:9090 bun run dev`。

## 测试

```bash
go test ./...
go test -race ./...
bun run test:e2e
```

`bun run test:e2e` 由 `playwright.config.ts` 启动两个进程：使用临时数据目录的 Go 服务（端口 18080）和模拟模型 `tests/mock-llm.mjs`（端口 18081）。
端口被占用时用例无法启动，先结束占用进程。缺浏览器时执行 `bunx playwright install chromium`。
