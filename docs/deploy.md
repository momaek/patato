# 部署与发布

## 一键部署（Docker Compose）

准备一台 Linux 机器（amd64 或 arm64），把三个域名解析到它，放行 80、443 端口：

```bash
curl -fsSL https://github.com/momaek/patato/releases/latest/download/install.sh | sudo bash -s -- --domain example.com
```

`--domain example.com` 等价于 `--api-host api.example.com --admin-host admin.example.com --portal-host my.example.com`，也可以分别指定。脚本会：

1. 没有 Docker 时用 get.docker.com 安装（`--no-install-docker` 关闭）；
2. 下载该版本的 `docker-compose.yaml`、`Caddyfile`、`config.example.yaml` 到 `/opt/patato`（`--dir` 修改）；
3. 首次安装生成 `.env`（版本、域名、端口、随机数据库密码）和 `config.yaml`（随机 `secret_key`，填好三个地址和 `webauthn.rp_id`）；
4. 拉取 `ghcr.io/momaek/patato-server:<版本>` 并启动 PostgreSQL、服务端、Caddy（自动签发证书）、每日备份；
5. 等服务端健康检查通过，首次安装时打印第一个运营者的邀请链接（`--operator` 指定名字，默认 `admin`）。

Telegram、SMTP、支付、tolato 等按需在 `/opt/patato/config.yaml` 里补上，然后 `cd /opt/patato && docker compose restart server`。

| 文件 / 目录 | 说明 |
| --- | --- |
| `.env` | 版本、镜像、三个域名、端口、数据库密码；Compose 和 Caddy 都从这里取值 |
| `config.yaml` | 服务端配置，只在首次安装时生成，之后由你维护 |
| `docker-compose.override.yaml` | 想改 Compose 时写在这里（升级不会动它） |
| `pgdata/`、`backups/`、`caddy-data/` | 数据库、备份（每日一份保留 14 天，升级前额外一份）、证书 |

### 升级

在同一台机器上再执行一次，不用带域名：

```bash
curl -fsSL https://github.com/momaek/patato/releases/latest/download/install.sh | sudo bash
# 指定版本 / 回滚：... | sudo bash -s -- --version v0.2.0
```

升级先把数据库备份到 `backups/pre-upgrade-*.dump`，备份失败就停止；`config.yaml`、`.env` 里的密码和数据不会被改。`docker-compose.yaml`、`Caddyfile` 换成新版本的，内容有变化时旧文件留成 `.bak`。数据库迁移在服务端启动时自动执行，回滚到旧版本前请先确认迁移是否兼容，必要时用升级前的备份恢复：

```bash
cd /opt/patato && docker compose stop server
docker compose exec -T postgres pg_restore -U patato -d patato --clean --if-exists < backups/pre-upgrade-<时间>.dump
```

### 其他用法

- 非标准端口：`--http-port 8080 --https-port 8443`，生成的地址会带上端口。
- 从源码构建镜像（在仓库的 `deploy/` 下）：`docker compose -f docker-compose.yaml -f docker-compose.build.yaml up -d --build`。
- 离线或测试：`--from-dir <放部署文件的目录> --image <镜像名> --no-pull` 不访问 GitHub、使用本机镜像。
- 只准备文件不启动：`--no-start`。

## 安装 CLI

```bash
curl -fsSL https://github.com/momaek/patato/releases/latest/download/install-cli.sh | sh
```

按系统和架构下载 `patato-{linux,darwin}-{amd64,arm64}`，用同一 Release 的 `checksums.txt` 校验后装到 `/usr/local/bin`（不可写时用 `~/.local/bin`），并建别名 `pat`。`--version`、`--dir` 可选。Windows 从 Release 页面下载 `patato-windows-*.exe`。

## patato-node

不需要单独安装：`patato machine install-cmd <机器>` 生成的命令会下载 `patato-node-linux-{amd64,arm64}`。`node.binary_url` 默认指向**与服务端同版本**的 Release（开发构建指向最新 Release），避免面板和节点版本不一致；节点访问不了 GitHub 时把它换成自己的镜像地址。

## 发布

推送 `vX.Y.Z` 形式的 tag 即可，`.github/workflows/release.yml` 会：

1. 跑 CLI、节点和配置相关测试，用 `scripts/release.sh` 构建 Release 附件并做冒烟检查；
2. 构建并推送多架构服务端镜像 `ghcr.io/momaek/patato-server:<tag>`（linux/amd64、linux/arm64），正式版同时打 `latest`；
3. 两者都成功后创建 GitHub Release，附件：

| 附件 | 用途 |
| --- | --- |
| `patato-{linux,darwin,windows}-{amd64,arm64}[.exe]` | CLI |
| `patato-node-linux-{amd64,arm64}` | 节点端，安装命令依赖这个文件名 |
| `install.sh`、`docker-compose.yaml`、`Caddyfile`、`config.example.yaml` | 一键部署，`install.sh` 按版本下载后三个 |
| `install-cli.sh` | CLI 安装脚本 |
| `checksums.txt` | 以上全部文件的 SHA-256 |

带 `-` 的 tag（如 `v0.2.0-rc.1`）发成预发布，不更新 `latest`，`install.sh` 默认不会装到它（可用 `--version` 显式指定）。也可以在 Actions 页面手动运行 release，填入已有 tag 重新发布。

本地预览附件：`make release VERSION=v0.1.0`，输出到 `dist/`。

首次发布后，需要在 GitHub 的 Packages 设置里把 `patato-server` 镜像设为 Public，否则服务器拉取镜像要先 `docker login ghcr.io`。
