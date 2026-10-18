# 部署

本页介绍如何长期运行这个站点：环境变量、二进制与容器镜像、Compose + HTTPS、可信反向代理，以及备份、恢复和升级。
最短的启动方式见 [`README.md`](../README.md)。

站点是单实例：SQLite 使用单连接，登录会话保存在进程内存里。每份数据目录只运行一个进程。
站点只有一个密码，登录后可以读写整个库。

## 环境变量

所有配置来自环境变量，模板见 [`.env.example`](../.env.example)。

| 变量 | 默认 | 用途 |
|---|---|---|
| `APP_PASSWORD` | 无，必填 | 站点密码。缺失时进程拒绝启动。 |
| `APP_ORIGIN` | `http://localhost:8080` | 浏览器地址栏里的地址。写请求按它校验 `Origin`，不一致返回 403 `bad_origin`。以 `https://` 开头时，会话 Cookie 带 `Secure`。 |
| `DATA_DIR` | `./data` | SQLite 目录，文件名固定为 `prompts.db`，目录权限按 `0700` 创建。 |
| `LISTEN_ADDR` | `127.0.0.1:8080` | 监听地址。直接运行二进制时只接受本机连接。 |
| `TRUST_PROXY` | `false` | 设为 `true` 时，登录限速读取 `X-Real-IP` 作为访客地址。条件见下节。 |
| `LLM_BASE_URL` | 空 | Go 后端访问的 OpenAI 兼容 API 根地址，后端请求 `{LLM_BASE_URL}/chat/completions`。可用公网域名或局域网地址，取决于运行后端的机器能否连上。 |
| `LLM_API_KEY` | 空 | 上游密钥，以 `Authorization: Bearer` 发送。 |
| `LLM_MODEL` | 空 | 上游模型名，记录在每次运行里。 |

`LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL` 三个都设置，优化功能才可用。缺任意一个时，服务照常启动，
登录、库和方案管理可用，新的优化请求返回 503 `model_not_configured`。
采样参数固定为 `temperature=0.2`、`top_p=1`、`max_tokens=2048`，记录在每次运行的 `params` 里。

容器镜像把 `LISTEN_ADDR` 设为 `0.0.0.0:8080`，`DATA_DIR` 设为 `/data`，运行容器时不必再传这两个变量。
`.env` 保存站点密码和模型密钥，用 `chmod 600 .env` 限制读取，它已被 `.gitignore` 忽略。

## 可信反向代理与 TRUST_PROXY

登录限速按访客地址计数。`TRUST_PROXY=false` 时只使用 TCP 对端地址，忽略 `X-Real-IP`、`X-Forwarded-For` 和 `Forwarded`。
经反向代理访问时，所有访客的对端地址都是代理，一个访客连续输错 8 次密码，所有人都会收到 429（`Retry-After: 600`）。

`TRUST_PROXY=true` 让服务按 `X-Real-IP` 计数，需要同时满足三个条件：

1. 应用端口只对反向代理开放，宿主机和外网都连不到它。
2. 反向代理对每个请求用连接对端 IP 覆盖 `X-Real-IP`。
3. 反向代理与应用通过内部网络连接。

仓库的 Compose 配置满足这三点：`app` 服务没有 `ports:`，Caddy 在 `deploy/Caddyfile` 里写 `header_up X-Real-IP {http.request.remote.host}`，两个容器在 Compose 内部网络互联。
服务只读取 `X-Real-IP`，且只接受单个合法 IP；开启后请求缺少该头或内容非法时，登录返回 400 `untrusted_client`。

应用端口对宿主机发布，或直接面向外网时，保持 `TRUST_PROXY=false`。给 `app` 服务加 `ports:` 之前，先把 `TRUST_PROXY` 改回 `false`。

## 二进制

从 GitHub Release 下载对应系统和架构的归档，核对并解包：

```bash
shasum -a 256 -c checksums.txt --ignore-missing
tar xzf prompts-web_0.1.0_linux_amd64.tar.gz
```

启动时设置环境变量。要接受其他机器的访问，把 `LISTEN_ADDR` 设为 `0.0.0.0:8080`，`APP_ORIGIN` 写用户在浏览器里输入的地址：

```bash
APP_PASSWORD='换成你的密码' APP_ORIGIN=http://你的主机名:8080 \
LISTEN_ADDR=0.0.0.0:8080 DATA_DIR=$HOME/prompts-web-data ./prompts-web
```

长期运行时交给 systemd、launchd 等进程管理器，把环境变量写进它的配置。

## 容器镜像

镜像 `ghcr.io/nerdneilsfield/prompts-web` 由 GoReleaser 在推送 `v*` 标签时发布，包含 `linux/amd64` 与 `linux/arm64`。
标签规则：稳定版 `v1.2.3` 得到 `v1.2.3` 和 `latest`；预发布版 `v1.2.3-rc.1` 只得到 `v1.2.3-rc.1`。
镜像内是单个静态二进制（网页已嵌入）、CA 证书和 uid 10001 的 `app` 用户，`/data` 是数据卷，默认监听 `0.0.0.0:8080`。

用 Docker 运行，数据保存在命名卷：

```bash
docker run -d --name prompts-web -p 8080:8080 \
  -e APP_PASSWORD='换成你的密码' \
  -e APP_ORIGIN=http://127.0.0.1:8080 \
  -v prompts-web-data:/data \
  ghcr.io/nerdneilsfield/prompts-web:latest
```

用宿主目录保存数据时，目录属主设为 10001：

```bash
sudo mkdir -p /srv/prompts-web && sudo chown 10001:10001 /srv/prompts-web
docker run -d --name prompts-web -p 8080:8080 \
  -e APP_PASSWORD='换成你的密码' -e APP_ORIGIN=http://127.0.0.1:8080 \
  -v /srv/prompts-web:/data ghcr.io/nerdneilsfield/prompts-web:latest
```

命名卷首次挂载时，Docker 把镜像里 `/data` 的内容和属主复制进卷。若卷由其他方式创建，属主是 root，启动日志出现 `unable to open database file`，先对卷执行一次 `chown 10001:10001 /data`。

后端访问 `LLM_BASE_URL` 的网络由容器所在主机决定。模型服务在宿主机或局域网上时，填容器内可达的地址。

Podman、Apple container 等 OCI 客户端使用同一个镜像，参数与上面一致。Apple container 的构建、运行和备份命令见 [`apple-container.md`](apple-container.md)。

## Compose + HTTPS

这个方案需要一个已解析到服务器的域名，80 和 443 端口可访问。

```bash
cp .env.example .env
chmod 600 .env
```

编辑 `.env`：填写 `APP_PASSWORD`、`APP_ORIGIN=https://你的域名`、`APP_DOMAIN=你的域名`，以及三个 `LLM_*` 变量。
然后启动：

```bash
docker compose up -d --build
```

`compose.yaml` 包含两个服务。`app` 不发布端口，`TRUST_PROXY` 设为 `true`，数据保存在命名卷 `app-data`。
`caddy` 监听 80 和 443，自动申请证书，把请求转发到 `app:8080`。
`deploy/Caddyfile` 把读写超时设为 120 秒，因为一次优化最长等待约 90 秒，请保持这个值不低于 90 秒。

## 备份与恢复

备份对象是 `DATA_DIR` 整个目录。备份前停止服务，`prompts.db` 在写入过程中复制可能不完整。
备份文件包含全部 Prompt 和方案，按敏感数据保管。

### 宿主目录

数据目录属于 uid 10001，权限 `0700`，用 `sudo` 读写：

```bash
docker stop prompts-web
sudo tar czf prompts-web-backup-$(date +%F).tar.gz -C /srv/prompts-web .
docker start prompts-web
```

恢复到一个新目录，用它启动：

```bash
sudo mkdir -p /srv/prompts-web-restored
sudo tar xzf prompts-web-backup-2026-10-18.tar.gz -C /srv/prompts-web-restored
sudo chown -R 10001:10001 /srv/prompts-web-restored
docker stop prompts-web && docker rm prompts-web
docker run -d --name prompts-web -p 8080:8080 \
  -e APP_PASSWORD='换成你的密码' -e APP_ORIGIN=http://127.0.0.1:8080 \
  -v /srv/prompts-web-restored:/data ghcr.io/nerdneilsfield/prompts-web:latest
```

解包会覆盖目标目录里同名文件，先确认目标目录是空的。用原密码登录，确认 Prompt 都在。

### Docker 命名卷

```bash
docker stop prompts-web
mkdir -p "$PWD/backups"
docker run --rm --user 0:0 -v prompts-web-data:/data:ro -v "$PWD/backups:/backup" \
  --entrypoint sh ghcr.io/nerdneilsfield/prompts-web:latest \
  -c 'tar czf /backup/prompts-web-backup.tar.gz -C /data .'
```

恢复到另一个卷：

```bash
docker volume create prompts-web-data-restored
docker run --rm --user 0:0 -v prompts-web-data-restored:/data -v "$PWD/backups:/backup" \
  --entrypoint sh ghcr.io/nerdneilsfield/prompts-web:latest \
  -c 'tar xzf /backup/prompts-web-backup.tar.gz -C /data && chown -R 10001:10001 /data'
```

用恢复出来的卷重建容器，参数与原容器一致，只替换卷：

```bash
docker rm prompts-web
docker run -d --name prompts-web -p 8080:8080 \
  -e APP_PASSWORD='换成你的密码' -e APP_ORIGIN=http://127.0.0.1:8080 \
  -v prompts-web-data-restored:/data ghcr.io/nerdneilsfield/prompts-web:latest
```

用原密码登录，确认 Prompt 都在。原卷 `prompts-web-data` 保留，需要时可以重新挂回。

`--entrypoint sh` 保留在命令里，`-c ...` 才会交给 shell 执行。`--user 0:0` 让 `tar` 读取卷内全部文件。

### Compose 命名卷

先查实际卷名。它由项目名和 `app-data` 组成，项目目录叫 `prompts-web` 时是 `prompts-web_app-data`：

```bash
docker compose config --format json \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["volumes"]["app-data"]["name"])'
```

停止 `app`，备份：

```bash
docker compose stop app
mkdir -p "$PWD/backups"
docker compose run --rm --no-deps --user 0:0 \
  -v "$PWD/backups:/backup" --entrypoint sh app \
  -c 'tar czf /backup/prompts-web-backup.tar.gz -C /data .'
```

`app` 服务已经挂载 `/data`，命令里只需要增加 `/backup`。

恢复到一个新卷：

```bash
docker volume create prompts-web_app-data-restored
docker run --rm --user 0:0 -v prompts-web_app-data-restored:/data \
  --entrypoint sh ghcr.io/nerdneilsfield/prompts-web:latest -c 'chown 10001:10001 /data'
docker run --rm --user 0:0 \
  -v prompts-web_app-data-restored:/data -v "$PWD/backups:/backup" \
  --entrypoint sh ghcr.io/nerdneilsfield/prompts-web:latest \
  -c 'tar xzf /backup/prompts-web-backup.tar.gz -C /data'
```

新建 `compose.restore.yaml`，让 `app-data` 指向恢复出来的卷：

```yaml
volumes:
  app-data:
    name: prompts-web_app-data-restored
```

此后每条 Compose 命令都带上两个 `-f`，包括升级：

```bash
docker compose -f compose.yaml -f compose.restore.yaml up -d app
docker compose -f compose.yaml -f compose.restore.yaml logs --tail 20 app
docker compose -f compose.yaml -f compose.restore.yaml stop app
```

不带 `compose.restore.yaml` 的命令会使用原卷 `prompts-web_app-data`，页面上会看到恢复之前的数据。
要去掉第二个 `-f`，把 `compose.yaml` 末尾的 `app-data:` 改成 `app-data: { name: prompts-web_app-data-restored }`，
用 `docker compose config` 确认卷名后再删除 `compose.restore.yaml`。

## 升级

1. 按上节备份。
2. 更新程序：镜像用 `docker pull ghcr.io/nerdneilsfield/prompts-web:latest` 后重建容器；二进制从 GitHub Release 下载归档，用同一页的 `checksums.txt` 核对 SHA256，再解包替换旧二进制；Compose 用 `docker compose up -d --build`。
3. 启动，访问 `/api/session`，用原密码登录，确认 Prompt 都在。

新版本启动时把库里遗留的 `running` 运行标记为 `interrupted`，并创建缺少的表，没有其他迁移步骤，数据目录保持不动。
回退时放回旧二进制，同时恢复升级前的备份。
