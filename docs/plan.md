# Patato 开发计划

> 2026-10-31 · 依据 [设计文档](design.md) 与 [界面设计稿](ui/README.md)

MVP 按 1 名工程师加 AI 辅助编码排期，约 8 周：1 周准备，7 周开发与上线。

**进度**：MVP 服务端、patato-node 和两个前端已完成，说明和已知限制见 [服务端说明](server.md) 与 [前端说明](web.md)。CLI 尚未开始。比设计文档最初估的 6 周多出的部分，是两个前端应用（Admin 9 页中的 6 页、用户侧 6 页）和迁移演练。

## 技术选型

能和 tolato 一致的都保持一致，便于复用代码和运维经验；有偏离的地方写明原因。

### 后端

| 用途 | 选型 | 说明 |
| --- | --- | --- |
| 语言 | Go（与 tolato 同步升级版本） | |
| HTTP 框架 | Gin | 与 tolato 一致 |
| API 契约 | OpenAPI 3.1（`api/openapi.yaml`）+ 契约测试 | 契约是前后端分离的单一事实来源，风险等级写在契约里（`x-risk`）。Gin 不支持路径中的冒号，路由改为手写，由测试保证与契约一致，见 [服务端说明](server.md) |
| 数据库 | PostgreSQL 16 | 与 tolato 一致 |
| 数据访问 | `pgx/v5` + `sqlc`（用 Docker 镜像生成） | **偏离 tolato 的 GORM**：计费、扣减、变更计划都需要显式事务、行锁和可审查的 SQL，ORM 的隐式行为在这里是风险 |
| 迁移 | `goose`，按版本号管理 | **偏离 tolato 的 AutoMigrate**：AutoMigrate 不能删改字段，生产库结构变更必须可审查、可回滚 |
| 任务队列 | `river`（基于 PostgreSQL） | 流量聚合、到期停用、审批过期、通知推送等异步任务。MVP 不引入 Redis，少一个要运维和备份的组件 |
| 鉴权 | `go-webauthn/webauthn`（passkey）、`golang-jwt/jwt/v5`、`golang.org/x/crypto`（bcrypt / argon2） | 运营者会话、终端用户会话、CLI token、节点密钥四类凭证，见设计文档「接口规范」 |
| Telegram | `go-telegram/bot` | 审批卡片、告警推送 |
| WebSocket | `gorilla/websocket` | 二期原生节点协议用，与 tolato 一致 |
| 日志 | 标准库 `log/slog` | |
| 指标 | `prometheus/client_golang` | 与 tolato 一致 |
| 配置 | YAML（`gopkg.in/yaml.v3`）+ 环境变量覆盖 | 与 tolato 一致 |
| 测试 | 标准库 `testing`、`testcontainers-go`（真实 PostgreSQL） | 计费、变更计划、迁移必须对真实数据库测试 |

### CLI

| 用途 | 选型 | 说明 |
| --- | --- | --- |
| 命令名 | `patato`，安装时附带别名 `pat` | |
| 实现 | Go，参照 tolato CLI 的结构（`auth login` 浏览器授权、profile 管理） | 复用 tolato 的登录流程代码 |
| API 调用 | `oapi-codegen` 生成的 Go 客户端 | 与服务端同一份契约，接口改了编译期就会报错 |

### 前端

Admin 和用户侧是两个独立应用，放在同一个 pnpm workspace 里共享组件和 API 客户端。

| 用途 | 选型 | 说明 |
| --- | --- | --- |
| 框架 | Vue 3.5 + TypeScript + Vite | 与 tolato 一致 |
| 组件 | shadcn-vue（reka-ui）+ Tailwind CSS 4 | 与 tolato 一致；设计 token 取自 [docs/ui](ui/README.md) 的设计约定 |
| 路由与状态 | Vue Router、Pinia | 与 tolato 一致 |
| 服务端数据 | `openapi-typescript` 生成类型 + `openapi-fetch` + TanStack Vue Query | **偏离 tolato 的 axios**：类型从契约生成，接口变化在类型检查时就能发现；Query 负责缓存和刷新 |
| 表格 | TanStack Vue Table | 与 tolato 一致 |
| 图标与提示 | `lucide-vue-next`、`vue-sonner` | 与 tolato 一致 |
| passkey | `@simplewebauthn/browser` | Admin 登录与审批 |
| 二维码 | `qrcode` | 用户侧订阅链接二维码 |
| 国际化 | `vue-i18n` | 用户侧先做中文，结构上预留英文 |
| 端到端测试 | Playwright | 只覆盖关键流程：登录、审批、导入订阅、重置链接 |

### 部署与工程

| 用途 | 选型 | 说明 |
| --- | --- | --- |
| 部署 | Docker Compose：`postgres`、`patato-server`、`caddy` | 与 tolato 的部署方式一致 |
| 前端托管 | 两个前端构建为静态文件，由 Caddy 托管并自动签发证书 | Admin 与用户侧可用不同域名，`/api` 反向代理到服务端 |
| 备份 | `pg_dump` 定时任务，异机保存，每月做一次恢复演练 | 计费数据丢失即资损 |
| CI | GitHub Actions：`golangci-lint`、`go test`、`vue-tsc`、构建镜像、检查生成代码是否与契约一致 | |

### 仓库结构

```
patato/
  api/openapi.yaml        API 契约
  server/                 服务端（cmd/patato-server）
  cli/                    CLI（cmd/patato）
  web/
    admin/                Admin 前端
    portal/               用户侧前端
    packages/ui/          共享组件与设计 token
    packages/api/         生成的 TypeScript 客户端
  node/                   二期 patato-node
  deploy/                 docker-compose.yaml、Caddyfile
  docs/
```

Go 代码用一个模块（`github.com/momaek/patato`），server、cli、node 共享生成的契约代码。

## 开工前需要定下的事

| 事项 | 影响 | 最晚 |
| --- | --- | --- |
| 迁移后的续费方案 | 用户侧是否需要续费入口，是否要把支付提前 | 第 3 周开始前 |
| MVP 是否开放注册 | 用户侧页面范围 | 第 3 周开始前 |
| 是否复用 tolato 的账号与鉴权 | 凭证体系怎么写。建议独立一套：Patato 有成千上万的终端用户，和 tolato 的运营者账号不是一类；登录也不应依赖 tolato 是否可用。实现上可以复制 tolato 的 auth 代码 | 第 1 周开始前 |
| 节点模板初始清单 | 意图级上节点接口 | 第 5 周开始前 |
| 审批人范围 | 审批模型是否支持多人 | 第 5 周开始前 |
| 自身监控与密钥轮换是否进 MVP | 第 7 周的工作量 | 第 6 周开始前 |

## MVP 排期

### 第 0 周：准备

- 梳理 Xboard 源码：UniProxy 接口的路由与字段、订阅接口按 UA 分发的逻辑、数据库结构（用户、套餐、节点、密码哈希算法）。
- 对一台 V2bX 节点抓包，核对梳理结果。
- 确认 tolato 侧已有能力：现有 `/api/v1` 节点查询是否返回 IP 和地区；审计接口能否用 API key 按主机查询执行记录。缺什么就在 tolato 排期补上。
- 仓库脚手架、CI、本地开发环境（Compose 起 PostgreSQL）。

关口：Xboard 接口与数据结构文档写完；仓库能跑通空的 CI。

### 第 1–2 周：底座与兼容层

- OpenAPI 契约骨架、代码生成流程、统一错误格式。
- 数据模型与第一批迁移：节点模板、节点、套餐、用户、流量上报、变更计划、审批、审计。
- 变更计划引擎：dry_run → `change_id`、受影响对象 `version` 校验、impact 计算、风险等级（含按影响面升级）、`Idempotency-Key`、审计。之后所有写接口都基于它。
- 凭证体系：运营者会话（passkey）、CLI token、节点密钥。
- UniProxy 适配层：拉配置、拉用户（含设备数与限速）、上报流量、上报在线 IP；按节点签发密钥，兼容旧全局 token 过渡。
- Xboard 迁移脚本第一版：用户、套餐、节点，token、UUID、密码哈希原样保留。

关口：一台节点上的 V2bX 不改代码连上 Patato，拉到迁移过来的用户。

### 第 3–4 周：业务主干与用户侧

- 意图级接口：provision、挂载 / 卸载、重置流量 / token；节点状态机与心跳。
- 订阅生成：通用 base64、Clash / Mihomo、sing-box，按 UA 分发，兼容 Xboard 订阅路径；只下发 online 节点。
- 流量计费：上报落库、异步聚合扣减、周期重置、超额与到期自动停用。计费逻辑先写测试再写实现。
- 用户侧 API（`/api/v1/me`）与终端用户会话。
- 用户侧前端 6 页：登录、我的订阅（含宽屏）、找回密码、设置新密码、重置订阅链接。

关口：计费测试全部通过；用真实客户端逐个验收三种订阅格式；小范围用户灰度，订阅可用、流量统计无异常。

### 第 5–6 周：Agent 化与 Admin

- 审批：审批请求与 `change_id` 绑定、Telegram 卡片与回调校验、网页审批 + passkey、CLI 拉起的 passkey 确认页。
- CLI 全部 MVP 命令，`--dry-run` / `--json` / `--wait`。
- `/skill.md` 与工具清单，从契约生成。
- 事件与告警：节点掉线、用户到期、流量超额推送 Telegram；面板主机执行告警（拉取 tolato 执行记录）。
- Admin 前端 6 页：总览、审批详情、节点、套餐、用户、审计日志。

关口：「把刚上线的东京机器加到高级套餐」由 Agent 全程执行；敏感操作必须经审批，CLI token 调用审批接口被拒绝。

### 第 7 周：迁移演练与上线

- 用生产数据副本完整演练迁移两次，对比用户数、订阅 token、剩余流量、到期时间。
- 备份与恢复演练；监控与告警上线。
- 切换手册与回切方案：DNS / 订阅域名切换步骤、回切触发条件。
- 按灰度、扩大灰度、全量三步切换。

关口：全量切换完成，老用户订阅链接照常可用；回切方案演练过一次。

## 二期排期

二期按业务优先级排，每项完成后单独上线。

| 顺序 | 内容 | 预估 | 前置条件 |
| --- | --- | --- | --- |
| 1 | 支付与订单：续费与购买、支付回调、退款审批；Admin 订单页 | 2 周 | 支付方式与退款规则确定 |
| 2 | 工单：用户侧工单、Admin 工单页、Agent 草拟回复 | 1.5 周 | |
| 3 | Admin 兜底的手动操作、报表 | 1 周 | |
| 4 | 邀请返佣与优惠券：返佣入账、自动冻结、解冻审批；Admin 返佣页 | 1.5 周 | 返佣规则确定 |
| 5 | patato-node：原生节点协议、machine 模式、带序号上报、经 tolato-agent 引导身份 | 4–6 周 | 首版内核确定；tolato-agent 提供本机身份证明接口 |
| 6 | 更多订阅格式、事件自动处置、多人审批 | 按需 | |

patato-node 放在后面，是因为 MVP 已经能用 V2bX 跑起来，它解决的是计费精度和配置下发速度。如果灰度期间流量统计误差或配置延迟成了实际问题，就把它提前。

## 质量要求

- 计费、变更计划、审批三块的测试覆盖到每个分支，用真实 PostgreSQL 跑。
- 所有写接口都要有 dry_run 测试：预演结果与实际执行结果一致。
- 契约改动必须同时更新生成代码，CI 检查不一致就失败。
- 每次上线前在预发环境用 V2bX 节点和三种客户端做一遍冒烟。
