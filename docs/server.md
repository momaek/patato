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

1. 按 `deploy/` 下的 Compose、Caddyfile 和 `config.example.yaml` 部署，填好 `admin_url`、`portal_url`、`webauthn.rp_id`。
2. 生成第一个运营者的邀请链接：`patato-server operator invite -name <名字>`，在 Admin 上用它注册 passkey。
3. CLI 登录：`patato auth login` 发起设备授权（`POST /api/v1/auth/cli/grants`），运营者在 Admin 上核对短码并批准，CLI 换取 token。
4. 配置 Telegram 后，运营者在 Admin 上生成绑定码（`POST /api/v1/auth/telegram:bind`），给机器人发送 `/bind <码>`，之后可以直接在 Telegram 里批准或拒绝。
5. 从 Xboard 迁移：先 `patato-server import-xboard -dsn '<user>:<pass>@tcp(<host>:3306)/<db>' -dry-run` 看报告，确认后去掉 `-dry-run`。迁移后在配置里填 `node.legacy_token`（Xboard 的 server_token），节点不改配置即可连上；之后逐台换成 `patato node install-cmd` 给出的节点密钥。

## 代码结构

| 目录 | 职责 |
| --- | --- |
| `api/openapi.yaml` | API 契约；`server/internal/api` 的测试保证路由表与它一致 |
| `server/cmd/patato-server` | 入口：`serve`、`migrate`、`operator invite`、`import-xboard` |
| `server/internal/change` | 变更计划引擎：预演、`change_id`、版本校验、按影响面升级风险、转审批、审计 |
| `server/internal/ops` | 所有写操作（节点、模板、套餐、用户），每个都是一个变更计划操作 |
| `server/internal/approval` | 审批：批准、拒绝、过期；只接受运营者会话和绑定过的 Telegram 账号 |
| `server/internal/auth` | 运营者 passkey 与会话、CLI 设备授权与 token、终端用户登录与找回密码 |
| `server/internal/uniproxy` | 与 Xboard 兼容的节点接口，按节点密钥鉴权 |
| `server/internal/subscribe` | 订阅：通用 base64、Clash / Mihomo、sing-box，按 UA 分发 |
| `server/internal/billing` | 流量聚合与扣减、周期重置、到期与超额、节点离线检测 |
| `server/internal/jobs` | River 周期任务与通知推送 |
| `server/internal/notify` | Telegram 审批卡片与告警、邮件 |
| `server/internal/xboard` | 从 Xboard 导入数据 |
| `server/internal/store` | 迁移（`migrations/`）、查询（`queries/`，sqlc 生成到 `db/`） |

## 与计划的差异

- **路由没有用 oapi-codegen 生成**：Gin 路由不支持路径里的冒号（`/nodes:provision`），生成的路由注册无法直接用。改为手写路由表，契约 `api/openapi.yaml` 仍是单一事实来源，由测试检查两者的方法、路径、`x-risk`、`x-access` 完全一致；对外的冒号路径在进入路由前改写成内部路径。
- **sqlc 用 Docker 镜像运行**：本机编译 sqlc 依赖的 C 库失败，`make generate` 改为调用 `sqlc/sqlc` 镜像。
- **`node install-cmd` 需要可写 token**：安装命令里含节点密钥，拿到它就能拉取节点上的全部用户凭证，所以不对只读 token 开放。设计文档里原标为只读。

## 已知限制

- **tolato 侧接口**：面板主机执行告警需要 tolato 用 API key 提供 `GET /api/v1/nodes/{id}/commands`（支持 `since`，沿用 tolato 的分页格式 `items`）；`/api/v1/nodes` 目前也不返回地区，`provision` 按机器名查询时带不出区域。tolato 侧的改动见 [momaek/tolato#27](https://github.com/momaek/tolato/issues/27)，补上之前 Admin 总览显示「tolato 尚未提供执行记录接口」。
- **订阅格式只做过内核校验**：生成的 sing-box 配置通过了 sing-box 1.11.15 的 `check`，Clash 配置通过了 mihomo 1.19.10 的 `-t`，覆盖 VLESS Reality、VMess WS、SS2022、Hysteria2 四种协议。计划第 3–4 周的关口还要求在真实客户端里导入并连通，这一步没做。端口跳跃（端口范围）和 Hysteria v1 暂不下发。
- **V2bX 安装命令未在真机验证**：安装脚本在无人值守时的行为需要实测。
- **Xboard 子节点**：子节点没有自己的节点后端，导入后不会有心跳，只在套餐里没有在线节点时下发；导入报告会逐个列出。只支持 Xboard 2025 年之后的统一节点表。
- **Telegram 绑定码保存在内存里**：多实例部署时要在同一个实例上生成和使用。
- **续费**：续费方案还在「待确认」里。MVP 后端已支持管理员手动续期（`PATCH /api/v1/users/{id}` 的 `extend_days`），自助续费与支付属于二期。

## patato-node

节点端代码在 `node/`，协议报文在 `nodeapi/`，服务端实现在 `server/internal/native`。内嵌 sing-box 1.13，构建需要标签 `with_quic,with_utls,with_acme`（`make build` 已带上）。

- 接入：`GET /api/v1/machines/{host}/install-cmd` 给出安装命令（含一小时内有效、只能用一次的注册码），在机器上执行后 `patato-node enroll` 换取机器 token 并装成 systemd 服务。机器接入后，`node.provision` 默认 `backend: native`，新节点通过 WebSocket 推送几秒内启动；节点在内核里跑起来并上报状态后才转为在线。
- 同步：按 `config_rev` / `users_rev` 只返回有变化的节点，用户能算出增量时只发增量，节点用 `users_rev` 自校验。
- 上报：先写本地队列再发送，面板按「机器 + 队列 + 序号」去重，同一事务里落库，流量精确入账一次。
- 限制：流量、限速、设备数由连接追踪器统一处理；移除或封禁用户立即断开现有连接；新增用户时 Shadowsocks 原地更新，其余协议重建 inbound（同一节点 30 秒内合并一次，会断开该节点的 UDP / QUIC 会话）。
- 独立运行：配置里写 `standalone: <文件>`，从本地文件读节点和用户，上报写到同目录 `reports.jsonl`。

已知限制：经 tolato-agent 的身份证明接入尚未实现（tolato 侧还没有接口），目前用面板生成的一次性注册码；只用 sing-box 内核，不支持 Hysteria v1、VLESS encryption、Shadowsocks 插件；二进制下载地址默认指向 GitHub Release（`node.binary_url`），发布流程还没加；没有在真实 Linux 机器和真实客户端上验证过。
