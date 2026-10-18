# Apple container 示例

[Apple container](https://github.com/apple/container) 是 macOS 上的 OCI 容器客户端。发布的镜像 `ghcr.io/nerdneilsfield/prompts-web` 与 Docker、Podman 通用，下面是 Apple container 的命令示例。环境变量和数据目录说明见 [`deploy.md`](deploy.md)。

## 运行发布的镜像

```bash
container system start
mkdir -p /tmp/prompts-web-data
container run -d --name prompts-web -p 8080:8080 \
  -e APP_PASSWORD='换成你的密码' \
  -e APP_ORIGIN=http://127.0.0.1:8080 \
  -v /tmp/prompts-web-data:/data \
  ghcr.io/nerdneilsfield/prompts-web:latest
```

## 从源码构建镜像

启动构建器：

```bash
container builder start
```

在仓库根目录构建镜像：

```bash
container build -f Dockerfile -t prompts-web:local .
```

镜像分三段：Bun 阶段执行 `bun install --frozen-lockfile` 与 `bun run build`；Go 阶段编译并嵌入 `web/dist`；
运行阶段是 `alpine:3.22`，只含静态二进制和 CA 证书，以 uid 10001 运行。

用本地镜像和宿主目录启动，端口 19080：

```bash
mkdir -p /tmp/prompts-web-data

container run -d --name prompts-web -p 19080:8080 \
  -e APP_PASSWORD='换成你的密码' \
  -e APP_ORIGIN=http://127.0.0.1:19080 \
  -v /tmp/prompts-web-data:/data \
  prompts-web:local
```

检查与停止：

```bash
curl -sS http://127.0.0.1:19080/api/session
container logs prompts-web
container stop prompts-web && container rm prompts-web
```

宿主目录挂载后，容器内可直接读写，`prompts.db` 属于宿主用户。
命名卷由 root 创建，挂到 `/data` 前需要把属主交给 10001：

```bash
container volume create prompts-web-data
container run --rm --uid 0 -v prompts-web-data:/data --entrypoint sh prompts-web:local \
  -c 'chown 10001:10001 /data'
container run -d --name prompts-web -p 19080:8080 \
  -e APP_PASSWORD='换成你的密码' -e APP_ORIGIN=http://127.0.0.1:19080 \
  -v prompts-web-data:/data prompts-web:local
```

## 备份与恢复

备份前停止服务，对象是 `DATA_DIR` 整个目录。

### 宿主目录

```bash
container stop prompts-web
tar czf /tmp/prompts-web-backup-$(date +%F).tar.gz -C /tmp/prompts-web-data .
container start prompts-web
```

恢复到一个新目录，用它启动：

```bash
mkdir -p /tmp/prompts-web-data-restored
tar xzf /tmp/prompts-web-backup-2026-10-18.tar.gz -C /tmp/prompts-web-data-restored
container stop prompts-web && container rm prompts-web
container run -d --name prompts-web -p 19080:8080 \
  -e APP_PASSWORD='换成你的密码' -e APP_ORIGIN=http://127.0.0.1:19080 \
  -v /tmp/prompts-web-data-restored:/data prompts-web:local
```

解包会覆盖目标目录里同名文件，先确认目标目录是空的。

### 命名卷

```bash
container stop prompts-web

mkdir -p /tmp/prompts-web-backups
container run --rm --uid 0 \
  -v prompts-web-data:/data:ro \
  -v /tmp/prompts-web-backups:/backup \
  --entrypoint sh prompts-web:local \
  -c 'tar czf /backup/prompts-web-backup.tar.gz -C /data .'
```

恢复到另一个卷：

```bash
container volume create prompts-web-data-restored
container run --rm --uid 0 -v prompts-web-data-restored:/data \
  --entrypoint sh prompts-web:local -c 'chown 10001:10001 /data'
container run --rm --uid 0 \
  -v prompts-web-data-restored:/data -v /tmp/prompts-web-backups:/backup \
  --entrypoint sh prompts-web:local \
  -c 'tar xzf /backup/prompts-web-backup.tar.gz -C /data'
```

`--entrypoint sh` 必须保留，否则 `-c ...` 会被当作服务参数。`--uid 0` 让 `tar` 读取卷内全部文件。
