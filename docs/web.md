# Patato 前端

> 对应 [开发计划](plan.md) 中 MVP 的两个前端：Admin（运营者）和用户侧。设计稿见 [docs/ui](ui/README.md)。

两个应用放在 `web/` 下的同一个 pnpm workspace 里，只通过 `/api/v1` 与服务端通信。

| 目录 | 内容 |
| --- | --- |
| `web/admin` | Admin：总览、审批、节点（含入口区块）、机器（含网络属性与转发流量）、线路、套餐、用户、订单、工单、优惠券、邀请返佣、报表、集成（Webhook 与自动处置）、审计日志、账户与凭证、CLI 授权、passkey 登录与邀请注册 |
| `web/portal` | 用户侧：登录、注册、我的订阅（含 30 天用量）、续费 / 购买、订单、工单、邀请、找回密码、设置新密码、重置订阅链接、修改密码 |
| `web/packages/ui` | 共享组件与设计 token（Tailwind 4 `@theme`，取自设计稿的颜色和字体约定） |
| `web/packages/api` | API 客户端、响应类型、格式化函数；`schema.d.ts` 由 `api/openapi.yaml` 生成 |

## 本地开发

先按 [服务端说明](server.md) 跑起 patato-server，配置里写：

```yaml
admin_url: "http://localhost:5173"
portal_url: "http://localhost:5174"
webauthn:
  rp_id: "localhost"
session:
  secure: false
```

```bash
cd web
pnpm install
pnpm dev:admin    # http://localhost:5173
pnpm dev:portal   # http://localhost:5174
```

Vite 把 `/api` 转给 `http://localhost:8080`（用环境变量 `PATATO_API` 改）。第一个运营者用 `patato-server operator invite -name <名字>` 生成的链接注册 passkey。

用户侧在登录前显示的服务名和客服联系方式来自构建时的环境变量 `VITE_SERVICE_NAME`、`VITE_SUPPORT_CONTACT`；登录后以服务端的 `service_name` 为准。

```bash
pnpm typecheck   # 四个包的类型检查
pnpm build       # 构建两个应用
pnpm gen:api     # 改了 api/openapi.yaml 后重新生成 schema.d.ts
```

## 部署

发布构建将两个前端通过 `go:embed` 嵌入 `patato-server`，前后端使用同一个应用镜像发布和回滚，不需要单独复制或挂载静态文件。PostgreSQL 和负责 HTTPS 的 Caddy 仍是独立容器。

```bash
# 在仓库根目录：构建两个前端及包含前端的服务端、节点端和 CLI
make build

# Docker 多阶段构建会自动安装前端依赖、构建并嵌入两个应用
docker compose -f deploy/docker-compose.yaml build server
docker compose -f deploy/docker-compose.yaml up -d
```

配置 `admin_url`、`portal_url` 为不同的域名（本地也可以是不同端口），不支持子路径。Caddy 将三个域名的请求原样反代到服务端，保留 Host；后端根据 Host 提供对应应用，未知域名不返回前端。两个前端的 `/api/*` 仍由同一服务处理，会话 cookie 留在各自的域名下，Origin 和 passkey 校验沿用原来的配置。

服务端支持 SPA 页面直接访问与刷新，带哈希的 `/assets/*` 缓存一年，首页不缓存；缺失资源返回 404，不回退到首页。`X-Frame-Options: DENY` 等安全响应头由服务端设置，审批页不能被嵌进别的页面。

`make web` 只构建前端，产物留在 `web/admin/dist` 和 `web/portal/dist`；之后可执行 `go build -tags embedweb ./server/cmd/patato-server`。不带 `embedweb` 标签的普通 `go run` / `go test` 不需要前端构建产物，开发时继续使用 Vite 热更新。

## 页面与服务端的约定

服务端生成的链接指向这些前端路由，改路由时要同时改服务端：

| 路由 | 来源 |
| --- | --- |
| Admin `/setup?invite=` | `patato-server operator invite` 输出的邀请链接 |
| Admin `/cli-auth?code=` | CLI 设备授权的 `verification_url` |
| Admin `/approvals/{id}` | Telegram 审批卡片；CLI 拉起确认页时带 `?via=cli`，审计里记为「CLI 拉起的 passkey」 |
| 用户侧 `/reset-password?token=` | 找回密码邮件 |
| 用户侧 `/orders/{订单号}` | 易支付的 `return_url`、Stripe Checkout 的成功/取消跳回地址；页面在待支付时每 3 秒刷新，跳回本身不会开通，等回调到账 |
| 用户侧 `/tickets/{id}` | 工单回复邮件 |
| 用户侧 `/register?invite=` | 邀请链接 |

## 与计划的差异

- **响应类型手写**：契约里大部分响应没有写 schema，生成的类型只用来约束请求路径（接口改名或删除时类型检查会失败），响应类型在 `packages/api/src/types.ts` 里按服务端视图手写。等契约补上响应 schema 后可以改成生成。
- **没有用 TanStack Table**：MVP 的表格只是展示和简单筛选，用普通表格加共享样式就够了。
- **组件没有用 shadcn-vue CLI 生成**：按它的做法（reka-ui + Tailwind + class-variance-authority）手写了需要的十来个组件，放在 `packages/ui`。
- **中文字体不从外网加载**：IBM Plex 只打包拉丁子集，中文用系统字体（苹方、微软雅黑等）。Google Fonts 在部分地区不可用。
- **国际化只做了用户侧**：用户侧文案都在 `portal/src/locales`，加英文只需要新增一份消息；Admin 只给运营者用，直接写中文。
- **比设计稿多的页面**：Admin 的「机器」（patato-node 接入状态）、「账户与凭证」（CLI token、备用 passkey、Telegram 绑定）、CLI 授权页、邀请注册页；用户侧的「修改密码」。二期的页面（订单、工单、优惠券、返佣、报表、集成；用户侧的购买、订单、工单、邀请、注册）没有设计稿，沿用现有页面的版式和组件。
- **二期起 Admin 提供兜底的手动操作**：用户、节点、套餐、订单、优惠券、返佣、集成页面的按钮打开同一个「预演 → 执行」对话框（`admin/src/components/ChangeDialog.vue`），调用的就是 CLI 用的写接口，敏感操作执行后跳到审批页。用户侧导航里「续费 / 购买」只在服务端开通在线支付时显示。
- **分流规则**：Admin 新增「分流规则」页（导入 ACL4SSR 模板、填 ini 地址或粘贴内容、立即拉取、查看策略组和规则文件缓存状态），套餐编辑里可以选默认规则；用户侧订阅页右栏可以从允许自选的规则里换一个。对着真实服务端在浏览器里走过：Admin 导入标准版（预演里看到从 GitHub 拉到的 11 个策略组、16 条规则）、套餐设默认规则，用户侧切到精简版后订阅里的规则随之变化。
- **报表的图表**：每日收入、每日流量是单一序列的柱状图（accent 色，悬停显示当天数值），同一份数据另有表格；节点成本只用表格。

## 已知限制

- **没有端到端测试**：计划里的 Playwright 关键流程测试还没写。这次是对着真实服务端在浏览器里逐页走过：Admin 各页、拒绝审批、批准 CLI 授权；用户侧登录（含输错密码）、二维码、重置订阅链接、找回密码到设置新密码。passkey 批准、登录和注册需要真实的验证器，没有在自动化浏览器里验证。
- **价格不带货币符号**：服务端按分存储，页面换算成元显示；货币由运营方决定，页面上不加符号。
- **一键导入的 URL scheme 没有在真机上试过**：Clash Verge、Shadowrocket、sing-box 用各自文档里的导入链接；v2rayN 没有导入 scheme，改为复制链接。
- **二期页面的验证**：对着真实服务端（PostgreSQL、内嵌前端）在浏览器里走过：用户侧登录、购买页试算、下单跳转支付、模拟易支付回调后订单页显示已开通、提交工单；Admin 的订单详情与退款预演、发起退款审批（需要一次性码的审批页）、工单里确认 Agent 草稿并发送、报表、套餐、集成、返佣页；用户侧在手机宽度下的购买页和工单页。没有真实支付商户，回调由脚本按易支付协议签名模拟。
