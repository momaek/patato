# Patato

Agentic 时代的代理面板：人只负责说话和审批，日常操作交给 AI Agent。

- 与 [tolato](https://github.com/momaek/tolato) 搭配：tolato 管机器，Patato 管节点、线路、套餐、用户和订阅。
- API 优先：所有写操作都可以预演（`--dry-run`），敏感操作需要人用 passkey 或 Telegram 批准。
- 节点兼容 Xboard / V2board 的 UniProxy 协议，V2bX / XrayR 无需改动即可接入；也可以使用自研节点端 patato-node（内嵌 sing-box，WebSocket 推送，经 tolato-agent 免密钥接入）。

## 功能

- **节点与订阅**：模板化建节点，在线状态与心跳；订阅支持通用 base64、Clash / Mihomo、Sing-box、Surge、Quantumult X、Loon，按客户端和地区下发；可选分流规则模板。
- **中转线路**：只需描述「这些节点走 广州 → 香港」，Patato 自动分配端口、下发和清理转发规则；可以随时改跳，第一跳可由多台机器共用一个入口域名；能看到每条线路的流量、每跳是否在线，故障时告警。
- **访问统计**：可选开启，查看每个节点、每个用户访问最多的网站和 IP。
- **售卖与运营**：套餐与调价、订单与支付（易支付、Stripe）、退款审批、工单、优惠券、邀请返佣、报表、风控、Webhook 与自动处置、审计日志。
- **两个网页**：运营后台（Admin）和用户侧，支持浅色 / 深色主题。
- **从 Xboard 迁移**：导入用户、套餐、节点、订单、邀请关系与余额。

## 安装

### 服务端

准备一台 Linux 机器（amd64 或 arm64），把三个域名解析到它，放行 80、443 端口，然后执行：

```bash
curl -fsSL https://github.com/momaek/patato/releases/latest/download/install.sh | sudo bash -s -- --domain example.com
```

`--domain example.com` 会使用 `api.example.com`、`admin.example.com`、`my.example.com`，也可以用 `--api-host`、`--admin-host`、`--portal-host` 分别指定。

脚本会安装 Docker（如果还没有），在 `/opt/patato` 下启动数据库、服务端、Caddy（自动签发 HTTPS 证书）和每日备份。完成后会打印第一个运营者的邀请链接，打开它在 Admin 上注册 passkey 即可登录。

Telegram、邮件、支付、tolato 等按需在 `/opt/patato/config.yaml` 里补上，然后重启：

```bash
cd /opt/patato && docker compose restart server
```

### 升级

在同一台机器上再执行一次，不用带参数：

```bash
curl -fsSL https://github.com/momaek/patato/releases/latest/download/install.sh | sudo bash
# 指定版本或回滚：... | sudo bash -s -- --version v0.2.0
```

升级前会自动备份数据库到 `/opt/patato/backups/`，`config.yaml` 和数据不会被改动。

### CLI

```bash
curl -fsSL https://github.com/momaek/patato/releases/latest/download/install-cli.sh | sh
patato auth login --url https://api.example.com
```

安装后同时有短别名 `pat`。登录时在 Admin 上核对短码并批准即可。Windows 从 [Releases](https://github.com/momaek/patato/releases) 下载 `patato-windows-*.exe`。

### 接入节点机器

patato-node 不需要单独安装。用 CLI 生成安装命令，在节点机器上执行（或交给 tolato 执行）：

```bash
patato machine install-cmd <机器名>
```

使用 V2bX / XrayR 的节点用 `patato node install-cmd <节点>` 获取对接命令。

### 从 Xboard 迁移

先预演，看过报告再正式导入：

```bash
cd /opt/patato
docker compose exec server patato-server import-xboard -dsn '<user>:<pass>@tcp(<host>:3306)/<db>' -dry-run
```

确认无误后去掉 `-dry-run`。迁移后在 `config.yaml` 里填 `node.legacy_token`（Xboard 的 server_token），原有节点不改配置即可连上。

## 状态

核心功能已在真机上验证可用，但稳定性验收尚未通过，暂不建议用于生产环境；尚未正式发布版本。
