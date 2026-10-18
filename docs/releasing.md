# 发布

本页写给维护者：先在本地构建完整快照并检查产物，再推送版本标签，由 GitHub Actions 发布。
使用者的部署与升级见 [`deploy.md`](deploy.md)。

发布产物是六个归档，对应 `linux`、`darwin`、`windows` 与 `amd64`、`arm64` 的组合。
前端已嵌入二进制，解包后直接运行，不需要 Bun、Node 或 Go。Linux 和 macOS 为 `tar.gz`，Windows 为 `zip`，另有一个 `checksums.txt`（SHA256）。
文件名格式 `prompts-web_<版本>_<系统>_<架构>`，例如 `prompts-web_1.2.3_linux_arm64.tar.gz`。

同一次发布还推送多架构镜像 `ghcr.io/nerdneilsfield/prompts-web`（`linux/amd64`、`linux/arm64`）。标签直接取 Git 标签：

| Git 标签 | 镜像标签 | GitHub Release |
|---|---|---|
| `v1.2.3` | `v1.2.3`、`latest` | 正式版 |
| `v1.2.3-rc.1` | `v1.2.3-rc.1` | 预发布 |

每个归档包含：二进制 `prompts-web`、`README.md`、`.env.example`、`docs/` 下的 `api.md`、`apple-container.md`、`deploy.md`、`development.md`、`evaluation.md`、`releasing.md`，以及 `docs/images/` 下的两张截图。

## 本地快照

```bash
goreleaser check
goreleaser release --snapshot --clean
```

`--snapshot` 不发布，也不需要 `GITHUB_TOKEN`。`--clean` 清空仓库内的 `dist/`。
`.goreleaser.yaml` 的 `before` 钩子依次执行 `bun install --frozen-lockfile`、`bun run typecheck`、`bun run build`，所以每次构建都从当前源码和锁文件重新生成 `web/dist`。
Go 以 `CGO_ENABLED=0` 编译，SQLite 驱动是纯 Go 实现。

检查产物：

```bash
ls dist/*.tar.gz dist/*.zip dist/checksums.txt
(cd dist && shasum -a 256 -c checksums.txt)
tar tzf dist/prompts-web_*_darwin_arm64.tar.gz
```

快照版本号形如 `0.0.1-next`，由 `snapshot.version_template` 生成。

本机没有 Docker daemon 时，快照加 `--skip=docker` 只生成归档：

```bash
goreleaser release --snapshot --clean --skip=docker
```

镜像由 `Dockerfile.release` 构建，构建上下文是 GoReleaser 生成的临时目录：`Dockerfile.release` 加上 `linux/amd64/prompts-web`、`linux/arm64/prompts-web` 两个预编译二进制，不含源码。
本地验证 arm64 镜像：把归档里的 `prompts-web` 放到 `ctx/linux/arm64/`，与 `Dockerfile.release` 同放在 `ctx/`，用任一 OCI 客户端构建。`ctx/` 放在用户目录下（Apple container 1.0 传输 `/tmp` 里的上下文时得到空内容）：

```bash
docker buildx build --platform linux/arm64 --build-arg TARGETPLATFORM=linux/arm64 \
  -f ctx/Dockerfile.release -t prompts-web-release-check:arm64 --load ctx
```

Apple container 把 `docker buildx build` 换成 `container build`，去掉 `--load`。

## 发布版本

```bash
git tag v1.2.3
git push origin v1.2.3
```

标签以 `v` 开头，GoReleaser 据此得到版本号。推送标签触发 [`.github/workflows/release.yml`](https://github.com/nerdneilsfield/prompts-web/blob/main/.github/workflows/release.yml)，工作流依次：

1. `actions/checkout` 以 `fetch-depth: 0` 取完整历史；
2. `actions/setup-go` 按 `go.mod` 安装 Go；
3. `oven-sh/setup-bun` 安装 Bun 1.4.3；
4. `docker/setup-qemu-action`、`docker/setup-buildx-action` 配置多架构构建；`docker/login-action` 用 `github.actor` 与 `GITHUB_TOKEN` 登录 `ghcr.io`；
5. `goreleaser/goreleaser-action` 执行 `release --clean`，把六个归档和 `checksums.txt` 附到该标签的 GitHub Release，推送多架构镜像，更新日志取自提交记录。

工作流的权限是 `contents: write` 和 `packages: write`，只在推送 `v*` 标签时运行。
首次发布后，在包页面 Package settings → Change visibility 改为 Public，才能匿名拉取。镜像带 `org.opencontainers.image.source` 标签，GitHub 据此把包关联到本仓库。

## 发布范围

- 发布内容是二进制归档和 GHCR 镜像。从源码自行构建镜像用根目录的 `Dockerfile`，Compose 方案（[`deploy.md`](deploy.md)）和 [`apple-container.md`](apple-container.md) 都用它。
- 二进制没有编译期版本变量，`ldflags` 只做 `-s -w` 去符号。版本号来自归档文件名，构建信息用 `go version -m prompts-web` 查看。
- 归档文件清单写在 `.goreleaser.yaml` 的 `archives.files` 里。真实 `.env`、数据目录和源码不在其中。
