# Patato 服务端

> 对应 [开发计划](plan.md) 中 MVP 的后端部分。前端见 [前端说明](web.md)，CLI 见 [CLI 说明](cli.md)。

## 本地运行

```bash
docker run -d --name patato-pg -e POSTGRES_USER=patato -e POSTGRES_PASSWORD=patato -p 127.0.0.1:5432:5432 postgres:16-alpine
cp deploy/config.example.yaml config.yaml   # 改 database_url、secret_key、public_url
go run ./server/cmd/patato-server -config config.yaml
```

启动时自动执行数据库迁移（业务表用 goose，任务队列表用 River）。

```bash
make test        # 集成测试：设置 PATATO_TEST_DATABASE_URL，或本机有 Docker（自动起 testcontainers）
make generate    # 改了 server/internal/store/queries 或 migrations 后重新生成查询代码（sqlc，走 Docker 镜像）
make lint
```

## 首次部署

1. 一键部署：`curl -fsSL https://github.com/momaek/patato/releases/latest/download/install.sh | sudo bash -s -- --domain example.com`，详见 [部署与发布](deploy.md)。脚本生成 `config.yaml`（含 `admin_url`、`portal_url`、`webauthn.rp_id`），两个前端随服务端镜像内嵌，Caddy 只负责 HTTPS 和反向代理。源码部署在 `deploy/` 下执行 `docker compose -f docker-compose.yaml -f docker-compose.build.yaml up -d --build`；本机构建完整二进制使用 `make build`（需要 pnpm）。
2. 一键部署会打印第一个运营者的邀请链接；手工部署时执行 `patato-server operator invite -name <名字>`（Compose 下为 `docker compose exec server patato-server operator invite -name <名字>`），在 Admin 上用它注册 passkey。
3. CLI 登录：`patato auth login` 发起设备授权（`POST /api/v1/auth/cli/grants`），运营者在 Admin 上核对短码并批准，CLI 换取 token。
4. 配置 Telegram 后，运营者在 Admin 上生成绑定码（`POST /api/v1/auth/telegram:bind`），给机器人发送 `/bind <码>`，之后可以直接在 Telegram 里批准或拒绝。
5. 从 Xboard 迁移：先 `patato-server import-xboard -dsn '<user>:<pass>@tcp(<host>:3306)/<db>' -dry-run` 看报告，确认后去掉 `-dry-run`。迁移后在配置里填 `node.legacy_token`（Xboard 的 server_token），节点不改配置即可连上；之后逐台换成 `patato node install-cmd` 给出的节点密钥。导入的节点没有机器名，要改由 patato-node 承载时一起填上：`patato node update <节点> --host <机器> --backend native`。

## 代码结构

| 目录 | 职责 |
| --- | --- |
| `api/openapi.yaml` | API 契约；`server/internal/api` 的测试保证路由表与它一致 |
| `server/cmd/patato-server` | 入口：`serve`、`migrate`、`operator invite`、`import-xboard` |
| `server/internal/change` | 变更计划引擎：预演、`change_id`、版本校验、按影响面升级风险、转审批、审计 |
| `server/internal/ops` | 所有写操作（节点、模板、套餐、用户），每个都是一个变更计划操作 |
| `server/internal/approval` | 审批：批准、拒绝、过期；只接受运营者会话和绑定过的 Telegram 账号 |
| `server/internal/auth` | 运营者 passkey 与会话、CLI 设备授权与 token、终端用户登录与找回密码 |
| `server/internal/uniproxy` | 与 Xboard 兼容的节点接口，按节点密钥鉴权；含 xboard-node 用的 `/api/v2/server/*`（handshake 关闭 WebSocket，走 REST 轮询；不支持 machine 模式） |
| `server/internal/subscribe` | 订阅：通用 base64、Clash / Mihomo、sing-box，按 UA 分发 |
| `server/internal/rules` | 分流规则：解析 subconverter 格式的 ini 模板（ACL4SSR 等）、按节点展开策略组、缓存并转换规则文件 |
| `server/internal/billing` | 流量聚合与扣减、周期重置、到期与超额、节点离线检测 |
| `server/internal/jobs` | River 周期任务与通知推送 |
| `server/internal/notify` | Telegram 审批卡片与告警、邮件 |
| `server/internal/xboard` | 从 Xboard 导入数据（含已完成订单、邀请关系、余额）；子节点导入为父节点的入口 |
| `server/internal/relay` | 中转线路：机器网络属性与端口分配、入口可用性与下一跳、链路发现解析、每跳在线与流量汇总等周期任务（见 [中转线路设计](relay.md)） |
| `server/internal/commerce` | 二期售卖：报价（灰度价格、优惠券、余额）、下单、易支付与 Stripe Checkout、回调履约、超时取消、返佣入账与风控 |
| `server/internal/automation` | 二期事件分发：Webhook 推送（HMAC 签名、失败重试）、自动处置规则 |
| `server/internal/store` | 迁移（`migrations/`）、查询（`queries/`，sqlc 生成到 `db/`） |

## 与计划的差异

- **路由没有用 oapi-codegen 生成**：Gin 路由不支持路径里的冒号（`/nodes:provision`），生成的路由注册无法直接用。改为手写路由表，契约 `api/openapi.yaml` 仍是单一事实来源，由测试检查两者的方法、路径、`x-risk`、`x-access` 完全一致；对外的冒号路径在进入路由前改写成内部路径。
- **sqlc 用 Docker 镜像运行**：本机编译 sqlc 依赖的 C 库失败，`make generate` 改为调用 `sqlc/sqlc` 镜像。
- **`node install-cmd` 需要可写 token**：安装命令里含节点密钥，拿到它就能拉取节点上的全部用户凭证，所以不对只读 token 开放。设计文档里原标为只读。

## 已知限制

- **tolato 侧接口**：面板主机执行告警需要 tolato 用 API key 提供 `GET /api/v1/nodes/{id}/commands`（支持 `since`，沿用 tolato 的分页格式 `items`）；`/api/v1/nodes` 目前也不返回地区，`provision` 按机器名查询时带不出区域。tolato 侧的改动见 [momaek/tolato#27](https://github.com/momaek/tolato/issues/27)，补上之前 Admin 总览显示「tolato 尚未提供执行记录接口」。
- **订阅格式尚未全部完成真机验收**：生成的 sing-box 配置通过了 sing-box 1.11.15 的 `check`，Clash 配置通过了 mihomo 1.19.10 的 `-t`，覆盖 VLESS Reality、VMess WS、SS2022、Hysteria2 四种协议。[真机核心验收](acceptance-2026-11-04.md) 已使用官方 sing-box 1.13.21 验证 Reality、SS2022、Hysteria2 的直连与中转，仍有间歇失败；其他协议及 Clash/Surge 真实客户端尚未完成验收。端口跳跃（端口范围）和 Hysteria v1 暂不下发。
- **V2bX 安装命令未在真机验证**：安装脚本在无人值守时的行为需要实测。
- **Xboard 子节点**：子节点没有自己的节点后端，导入后不会有心跳，只在套餐里没有在线节点时下发；导入报告会逐个列出。只支持 Xboard 2025 年之后的统一节点表。
- **Telegram 绑定码保存在内存里**：多实例部署时要在同一个实例上生成和使用。
- **续费**：二期已提供在线支付（见下文）；没有配置支付方式时，仍由管理员手动续期（`PATCH /api/v1/users/{id}` 的 `extend_days`）。

## patato-node

节点端代码在 `node/`，协议报文在 `nodeapi/`，服务端实现在 `server/internal/native`。内嵌 sing-box 1.13，构建需要标签 `with_quic,with_utls,with_acme`（`make build` 已带上）。

- 接入：`GET /api/v1/machines/{host}/install-cmd` 给出安装命令（含一小时内有效、只能用一次的注册码），在机器上执行后 `patato-node enroll` 换取机器 token 并装成 systemd 服务。机器接入后，`node.provision` 默认 `backend: native`，新节点通过 WebSocket 推送几秒内启动；节点在内核里跑起来并上报状态后才转为在线。
- 同步：按 `config_rev` / `users_rev` 只返回有变化的节点，用户能算出增量时只发增量，节点用 `users_rev` 自校验。
- 上报：先写本地队列再发送，面板按「机器 + 队列 + 序号」去重，同一事务里落库，流量精确入账一次。
- 限制：流量、限速、设备数由连接追踪器统一处理；移除或封禁用户立即断开现有连接；新增用户时 Shadowsocks 原地更新，其余协议重建 inbound（同一节点 30 秒内合并一次，会断开该节点的 UDP / QUIC 会话）。
- 独立运行：配置里写 `standalone: <文件>`，从本地文件读节点和用户，上报写到同目录 `reports.jsonl`。

接入方式有两种：面板生成的一次性注册码（默认），或经 tolato-agent 引导身份（见下文「经 tolato-agent 接入」）。

已知限制：只用 sing-box 内核，不支持 Hysteria v1、VLESS encryption、Shadowsocks 插件；二进制下载地址默认指向与服务端同版本的 GitHub Release（`node.binary_url`，见 [部署与发布](deploy.md)）；已在真实 Linux 机器和真实客户端进行 [核心验收](acceptance-2026-11-04.md)，但 DNS 负缓存、间歇 HTTPS/UDP 失败及正式证书等问题尚未收口，不能据此宣布生产就绪。

## 二期：售卖、工单、运营

配置项见 `deploy/config.example.yaml` 的 `payment`、`registration`、`approval`、`anomaly`、`trusted_proxies`。

**订单与支付**

- 用户在用户侧下单（新购、续费、换套餐、流量重置包），价格以服务端试算为准：灰度价格 → 优惠券 → 余额抵扣，实付为 0 时直接开通。待支付订单占用优惠码次数和余额，超时（`payment.order_timeout`）自动取消并退回。
- 支付驱动支持易支付（彩虹易支付及兼容接口，MD5 签名）和 Stripe Checkout（托管收银台、人民币单次付款）。回调地址 `{public_url}/api/v1/payment/{code}/notify`：验签通过、支付方式和金额与订单一致才开通，回调记在 `payment_callbacks`，平台重发或并发回调不会重复开通；超时取消后才到账的订单照常开通。金额不符、套餐已被删除时不开通并发出 `order_needs_attention` 告警。回调请求体上限为 1 MiB。
- Stripe 配置 `driver: stripe`、`secret_key`（API secret key，不是 publishable key）、`webhook_secret`（该端点的 signing secret），示例见 `deploy/config.example.yaml`。在 Stripe 为上述地址创建 POST Webhook，订阅 `checkout.session.completed`、`checkout.session.async_payment_succeeded`；密钥与端点必须属于同一测试/正式环境。反向代理必须保留原始 JSON 请求体和 `Stripe-Signature`。服务端校验签名和 5 分钟时间戳容差，只接受 `mode=payment`、`currency=cny`、`payment_status=paid` 的匹配订单；未支付状态及其他签名有效的事件只确认接收。成功和取消都会跳回用户侧订单页，页面返回不会触发履约。
- Checkout 金额取服务端优惠及余额抵扣后的实付（分），关闭 Adaptive Pricing，不提供自动换汇或 Stripe 订阅扣款。重复打开同一订单的 Stripe 付款链接使用相同幂等键；创建 Session 失败时订单仍保持待支付，可在订单页重试。Patato 取消/超时不会自动关闭 Stripe Session；付款链接已过期或已付款但换过支付方式时，需要运营者核实处理，不会自动退款。
- 续费从当前到期日接着算；已经到期后续费从现在算并重置流量周期。换套餐立即切换、到期时间从支付时重新计算，原套餐剩余时长不折算（设计文档「二期售卖规则」里还待确认）。
- 退款（`order.refund`）固定为敏感操作：默认退实付全额、扣回这笔订单顺延的天数、退回抵扣的余额、撤销返佣（已入账的从邀请人余额扣回）。资金需要在支付平台原路退回，易支付没有统一的退款接口。回调丢失时用 `order.mark_paid` 手动确认收款，同样要审批。
- 优惠券：减金额或打折，可限套餐、周期、总次数、每人次数、时间窗口。发券和改券是敏感操作，只停用不需要审批。

**邀请返佣**：规则存在 `settings` 的 `referral.rules`，改规则走审批。被邀请人付款后按实付金额生成返佣，`wait_days` 天后入账到邀请人余额，续费时抵扣（余额不支持提现）。同一邀请人 24 小时内邀请的同网段（IPv4 /24、IPv6 /48）账号超过阈值时返佣自动冻结，事件出现在总览「需要你处理」里，解冻走审批。用户侧注册由 `registration.enabled` 控制，同一 IP 每小时最多注册 5 次。

**工单**：用户在用户侧提交和回复。Agent 用 `ticket.draft` 保存草稿（服务端写入草拟人：哪个 token、自报的哪个会话，以及依据了哪些数据），或标为「等你处理」；发送回复（`POST /tickets/{id}:reply`）只接受运营者会话，发送后邮件通知用户。

**Admin 兜底的手动操作**：Admin 的用户、节点、套餐、订单、优惠券、返佣、集成页面都能直接操作，和 CLI 一样先预演、再按 `change_id` 执行，敏感操作转审批。没有只有网页能做的操作。

**报表**：流量（每日、用量最多的用户）、收入（每日收入与退款、按套餐）、节点成本（月成本按天数折算，每 GB 成本；成本用 `node update --monthly-cost` 设置）。

**事件、Webhook 与自动处置**

- 新增事件：`user_expiring`（`anomaly.expiring_days` 天内到期，可配合规则发提醒邮件）、`traffic_anomaly`（某天用量超过前 7 天日均 `anomaly.factor` 倍且不少于 `anomaly.min_gb`，常见于账号共享）、`order_paid`、`order_refunded`、`order_needs_attention`、`referral_frozen`、`ticket_created`、`ticket_replied`。
- Webhook：事件 POST 到外部地址，带 `X-Patato-Signature: sha256=HMAC(密钥, 时间戳 + "." + 请求体)` 和 `X-Patato-Timestamp`，失败由任务队列重试。新建是敏感操作（事件里有用户邮箱）；签名密钥只给运营者会话查看。
- 自动处置规则：`email_user`（给事件对应的用户发邮件）、`operation`（以系统身份执行一个变更操作，参数可用 `{{subject_id}}`、`{{payload.字段}}` 占位）、`replace_offline_node`（节点掉线时把同地区带 `standby` 标签的在线节点挂到它所在的套餐）。动作走变更计划，风险等级照常生效，被判为敏感时只生成审批。新建和修改规则是敏感操作，只停用不需要审批。

**多人审批与一次性码**：`approval.required` / `approval.required_for` 设置需要几名运营者批准，每人一票，票数够了才执行，任何一人拒绝即作废。`approval.code_for` 里的操作批准时除了 passkey 还要输入一次性码：运营者在审批页点「发送一次性码」，码经 Telegram 私聊发给他本人（需要先绑定），5 分钟有效、错 5 次作废；这类审批不能在 Telegram 卡片里批准。

**订阅**：新增 Surge（含 Surfboard，托管配置可自动更新）、Quantumult X、Loon 三种格式，按 UA 或 `flag` 分发；`region` 参数按节点地区下发（多个用 `|` 分隔）。

**分流规则**（`rule_profiles`）：管理员导入 subconverter 格式的 ini 模板，内置了 ACL4SSR 的 7 个在线版模板（`GET /rule-profiles/presets`，`preset` 一键导入），也可以填任意 ini 地址或直接粘贴内容。只使用其中的 `ruleset` 和 `custom_proxy_group`，其余键忽略；`!!GROUPID` 这类成员和 `quanx:` 等规则来源会跳过并在详情里列出。

- 生效顺序：用户自选（且这份规则仍允许自选）→ 套餐默认（`plan.update --rule-profile`）→ 内置的简单规则（国内直连，其余走代理，与之前一致）。用户在用户侧只能从 `user_selectable` 的规则里选，换规则不需要重新导入订阅，客户端下次更新时生效。模板拉取失败或解析失败时这些用户临时拿到内置规则，订阅不会坏。
- 策略组按订阅里实际下发的节点展开：正则匹配节点名；一个节点都匹配不到的组放 `DIRECT`（与 subconverter 一致）。
- 规则文件（`.list`）不直连 GitHub：服务端缓存在 `rule_lists`，从订阅域名的 `/api/v1/client/rules/{key}.{yaml|list|json}` 下发，分别对应 Clash 的 `rule-providers`（classical）、Surge 的 `RULE-SET`、sing-box 的远程规则集（source 格式），各自跳过客户端不支持的规则类型。第一次被请求时下载；之后后台任务每小时检查一次，超过 6 小时的模板和规则文件重新拉取，拉取失败保留旧内容；不再被任何模板引用的规则文件会被清理。服务端访问 GitHub 需要代理时设置 `HTTPS_PROXY`。
- 只对 Clash / mihomo、sing-box、Surge 生效；Quantumult X、Loon 和通用 base64 仍是原来的输出。

**套餐版本与灰度调价**：每次修改套餐前保存一份快照（`GET /plans/{id}/revisions`）。灰度调价时约 `rollout_percent`% 的用户（按用户和套餐哈希，结果稳定）下单时看到 `rollout_prices`，确认后 `promote_rollout` 转正或 `end_rollout` 结束；已付款订单不受影响。

**审计导出**：`GET /api/v1/audit/export` 按时间、动作、入口等筛选，导出 CSV 或 JSON，最多 10,000 条。

**迁移**：`import-xboard` 现在也导入已完成的订单（`v2_order` 状态 3、4，金额沿用 Xboard 的分）、邀请关系（`invite_user_id`）和余额（`balance` + `commission_balance`）。

**经 tolato-agent 接入**（`tolato.attest_enroll: true`）：安装命令里不再带注册码，而是 `patato-node enroll --tolato`。patato-node 从本机 tolato-agent 的 systemd unit 读出 `--server`、`--data-dir`，运行 `tolato-agent --attest <面板地址>`；tolato-agent 用自己的节点凭证（只有 root 可读）向 tolato 要一份只对这个面板有效、5 分钟内只能用一次的证明。Patato 拿证明调用 tolato 的 `/api/v1/attestations/verify` 核实，按 tolato 里的名称、别名或 IP 找到机器并签发机器 token。只接受已接入的机器（换发 token）或安装窗口开着的机器（生成安装命令时打开一小时），吊销后要重新生成安装命令才能再接入。

**落地访问统计**（`monitoring.destinations`，默认关闭，见 [监控计划](monitoring.md) 阶段 4）：开启后 sync 的 `settings.destinations` 为 true，patato-node 在一个同步周期内开始（关闭时停止）嗅探 TLS SNI、HTTP Host、QUIC，按「用户 × 主域名或 IP」聚合，每 5 分钟随上报带一次，与流量在同一个事务里按序号去重，写进 `destination_stats_hourly`。`GET /nodes/{id}/destinations`（按用户求和，`?user=` 筛选）、`GET /users/{id}/destinations`（`?node=` 筛选），`?since=24h|7d` 不超过保留天数；周期任务 `destinations_retention` 每小时删除超过 `retention_days` 的数据。开启时用户侧「我的订阅」页显示统计范围与保留期限。

### 二期已知限制

- **支付尚未完成真实商户联调**：易支付签名和验签用测试对照了 Xboard 线上在用的 EPay 实现，空参数兼容官方 SDK 和 Xboard 两种签法。Stripe 使用官方 Go SDK，测试覆盖模拟 Stripe HTTP 接口、真实 PostgreSQL 下单、验签、金额/币种/方式拒绝、延迟支付和并发幂等，但这不等于 Stripe 沙盒或正式商户验收；启用前仍需用商户测试环境验证支付、Webhook 可达性，以及账户地区、人民币收款能力和最低金额等限制。
- **退款不自动原路退回**：易支付没有统一退款接口，本期也未调用 Stripe Refund API；Patato 记录退款并扣回时长，资金由运营者在支付平台操作。
- **换套餐剩余时长不折算、返佣不能提现**：设计文档「二期售卖规则」还没确定，按最简单的规则实现。
- **一次性码只经 Telegram 私聊下发**：没有配置 Telegram 时，`approval.code_for` 里的操作无法批准。
- **新订阅格式没有在 Surge、Quantumult X、Loon 真机上导入过**（这些客户端只有 iOS / macOS 版）。字段写法逐项对照了 Xboard 的 Surge、QuantumultX、Loon 生成器，下发的协议范围也与它一致：Surge 和 Loon 不下发 VLESS、Shadowsocks 2022 和带混淆的 Hysteria2。超出 Xboard 的两处按客户端文档实现、没有对照：Surge 的 TUIC（`tuic-v5`）、Quantumult X 的 VLESS（含 Reality）。
- **Trojan 的 SNI**：Xboard 把 Trojan 的 `server_name`、`allow_insecure` 放在协议设置顶层，其他协议放在 `tls_settings` 下。之前各订阅格式和 patato-node 只读 `tls_settings`，从 Xboard 迁移来的 Trojan 节点会丢 SNI；现在两处都认，顶层优先。
- **经 tolato 接入依赖 tolato 的新接口**：需要 tolato 带上 `POST /api/agent/attestations`、`POST /api/v1/attestations/verify` 和 `tolato-agent --attest`（tolato 仓库的 `feat/agent-attestation` 分支）。tolato 没升级前保持 `tolato.attest_enroll: false`，继续用注册码。
- **替换掉线节点不会自动撤回**：节点恢复后，备用节点仍挂在套餐上，需要手动卸载。
- **分流规则只用 sing-box 自身校验过**：sing-box 输出在测试里交给 sing-box 1.13 的配置解析和 `box.New` 检查（出站、规则集引用都存在）；ACL4SSR 的 9 个在线版模板都能无警告解析。Clash（mihomo）和 Surge 的输出按格式文档生成，没有在真实客户端里导入过。
- **sing-box 没有拦截出站**：1.13 起去掉了 `block` 出站，默认选 REJECT 的策略组（如「全球拦截」「应用净化」）不生成出站，命中的规则直接 `reject`，用户不能在客户端里把它切成直连；其他组里的 REJECT 成员去掉。`GEOIP,CN` 等用 SagerNet 在 GitHub 上的 `geoip-*.srs` / `geosite-*.srs`，经第一个策略组下载。
