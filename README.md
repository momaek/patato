# Patato

[![License](https://img.shields.io/github/license/momaek/patato)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/momaek/patato)](go.mod)
[![Stars](https://img.shields.io/github/stars/momaek/patato?style=social)](https://github.com/momaek/patato/stargazers)

**Agentic 时代的机场面板 / 代理面板，可作为 Xboard、V2board 的替代品。** 人只负责说话和审批，节点、中转线路、套餐、用户和订阅这些日常操作交给 AI Agent。

Go 编写，Docker 一键部署；兼容 Xboard / V2board 的 UniProxy 节点协议，V2bX、XrayR 不改代码即可接入；支持从 Xboard 一键迁移，老用户订阅链接不变。

[English](#english) · [功能](#功能) · [和 Xboard 的区别](#和-xboard--v2board-的区别) · [安装](#安装) · [从 Xboard 迁移](#从-xboard-迁移)

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

**支持的协议**：VLESS（含 Reality）、VMess、Trojan、Shadowsocks、Hysteria / Hysteria2、TUIC、AnyTLS。

## 和 Xboard / V2board 的区别

| | Xboard / V2board | Patato |
| --- | --- | --- |
| 技术栈 | PHP（Laravel） | Go，单个服务端镜像，内嵌两个前端 |
| 日常运维 | 人在后台点表单 | AI Agent 通过 API / CLI 操作，人只审批 |
| 写操作安全 | 直接生效 | 先预演出计划，敏感操作需 passkey 或 Telegram 批准，全量审计 |
| 节点密钥 | 全站共享一个 server token | 每个节点单独签发，泄露一台不影响其他 |
| 节点端 | V2bX / XrayR | 兼容 V2bX / XrayR，另有自研 patato-node（sing-box 内核，WebSocket 推送） |
| 中转 | 需自己配转发 | 描述线路即可，自动分配端口、下发规则、监控每一跳 |
| 迁移 | — | 内置 Xboard 导入，token、UUID、密码哈希原样保留 |
| 部署 | 一个域名，后台挂在隐藏路径 | 同样只需一个域名，Admin 挂在随机路径；Docker 一键部署，自动 HTTPS 与每日备份 |

## 安装

### 服务端

准备一台 Linux 机器（amd64 或 arm64），把一个域名解析到它，放行 80、443 端口，然后执行：

```bash
curl -fsSL https://github.com/momaek/patato/releases/latest/download/install.sh | sudo bash -s -- --host panel.example.com
```

API、订阅和用户侧都在 `https://panel.example.com`，Admin 挂在一段随机路径下（例如 `https://panel.example.com/4d2d9ed0b64d`），安装结束时会打印出来，请妥善保存、不要公开。想自己定路径用 `--admin-path`。

也可以用三个子域名：`--domain example.com` 会使用 `api.example.com`、`admin.example.com`、`my.example.com`，或用 `--api-host`、`--admin-host`、`--portal-host` 分别指定。

#### 一个域名还是独立 Admin 域名？

| | 一个域名（`--host`） | 独立 Admin 域名（`--domain` 等） |
| --- | --- | --- |
| 需要准备 | 1 个域名 | 2–3 个域名 |
| Admin 的位置 | 用户侧域名下的随机路径，知道路径的人能找到登录页 | 单独的域名，用户侧完全看不到后台 |
| passkey 绑定的域名（`webauthn.rp_id`） | 这个域名本身 | 用 `--domain example.com` 时是 `example.com`；分别指定 `--admin-host` 时是 Admin 域名本身 |

**先选好再上线。** passkey 绑定在 `webauthn.rp_id` 上，只能在这个域名和它的子域名下使用，与路径无关。之后如果把 Admin 换到不在 `rp_id` 之下的域名（例如从 `admin.a.com` 改成 `panel.b.com/xxx`），已注册的 passkey 会全部失效，只能改 `rp_id` 后用 `patato-server operator invite` 重新邀请运营者、重新注册。在 `rp_id` 之下换子域名或改路径则不受影响；只升级版本、不改地址时也不受影响。

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

升级前会自动备份数据库到 `/opt/patato/backups/`，`config.yaml` 和数据不会被改动，域名、Admin 路径和已注册的 passkey 都保持不变。

### CLI

```bash
curl -fsSL https://github.com/momaek/patato/releases/latest/download/install-cli.sh | sh
patato auth login --url https://api.example.com
# 一个域名的部署：服务端不会把隐藏的 Admin 路径告诉未登录的 CLI，需要自己带上
patato auth login --url https://panel.example.com --admin-url https://panel.example.com/<Admin 路径>
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

已发布 [v0.1.0](https://github.com/momaek/patato/releases)。核心功能已在真机上验证可用，但稳定性验收尚未通过，暂不建议用于生产环境。

## License

[Apache-2.0](LICENSE)

---

## English

**Patato is an agent-first proxy panel (airport panel) — a modern alternative to Xboard and V2board, written in Go.** Humans only chat and approve; AI agents handle nodes, relay routes, plans, users and subscriptions through the API and CLI.

- **Xboard / V2board compatible**: implements the UniProxy node API, so V2bX and XrayR connect without code changes. Built-in Xboard importer keeps user tokens, UUIDs and password hashes, so existing subscription links keep working.
- **Safe by design**: every write can be dry-run as a plan first; sensitive changes require passkey or Telegram approval; full audit log. Per-node API keys instead of one shared server token.
- **Protocols**: VLESS (incl. Reality), VMess, Trojan, Shadowsocks, Hysteria / Hysteria2, TUIC, AnyTLS.
- **Subscriptions**: base64 (v2rayN / Shadowrocket), Clash / Mihomo, sing-box, Surge, Quantumult X, Loon.
- **Relay routes**: describe "these nodes go Guangzhou → Hong Kong" and Patato allocates ports, pushes forwarding rules and monitors every hop.
- **Billing and ops**: plans, orders, EPay and Stripe, refunds, tickets, coupons, referral commission, reports, webhooks.
- **Own node agent (optional)**: patato-node with embedded sing-box, WebSocket push, zero-key onboarding via [tolato](https://github.com/momaek/tolato).

One-command install (Docker Compose, automatic HTTPS, daily backups):

```bash
curl -fsSL https://github.com/momaek/patato/releases/latest/download/install.sh | sudo bash -s -- --host panel.example.com
```

Only one domain is needed: the API, subscriptions and the user portal live on it, and the admin panel sits under a random secret path printed at the end of the install (like Xboard). Use `--domain example.com` for separate `api.` / `admin.` / `my.` subdomains instead.

Choose before going live: passkeys are bound to `webauthn.rp_id` (the domain, not the path). Moving the admin panel to a domain outside the current `rp_id` invalidates every registered passkey; upgrades that keep the same addresses do not.

Status: [v0.1.0](https://github.com/momaek/patato/releases) released. Core features verified on real machines; stability acceptance not yet passed, not recommended for production yet.
