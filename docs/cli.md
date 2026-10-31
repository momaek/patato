# patato CLI

> 对应 [开发计划](plan.md) 第 5–6 周的 CLI。命令清单见设计文档「CLI 设计」，给 Agent 的用法见服务端的 `/skill.md`。

`patato` 是 `/api/v1` 的薄封装，命名和交互方式与 tolato CLI 一致；短别名 `pat`。它能做什么由服务端和 token 决定：写命令都可以 `--dry-run` 预演，敏感操作返回审批，由人用 passkey 或 Telegram 批准，CLI 自己完成不了批准。

## 安装与登录

```bash
make install-cli                      # 安装到 $(go env GOPATH)/bin，同时建 pat 别名
patato auth login --url https://api.example.com
```

登录用设备授权：CLI 打印一个短码和 Admin 上的链接（默认自动打开浏览器），运营者用 passkey 登录 Admin、核对短码后批准，CLI 拿到 token 写进 `~/.config/patato/config.yaml`（权限 0600）。

- `--scope read`：只能查询；`--scope write`（默认）：还能预演和执行变更，敏感变更仍需审批。
- 多个部署用 profile 区分：`patato profile list | use | remove`，单次用 `--profile <名字>`。
- `patato auth logout` 先在服务端吊销 token，再删掉本地的；token 只能吊销自己。
- 不想写配置文件时可以用环境变量 `PATATO_URL` 和 `PATATO_TOKEN`（显式 `--profile` 时不生效，避免过期的变量把命令打到别的部署上）。

## 命令

```
patato status
patato node list | show | provision | update | attach | detach | rotate-key | delete | install-cmd
patato machine list | show | install-cmd | revoke
patato template list | show | create | update
patato plan list | show | create | update | delete
patato user list | show | create | update | reset-traffic | reset-token | delete
patato change show | apply
patato approval list | show | approve
patato audit log
patato events list | watch
patato usage
```

每条命令加 `-h` 看选项。几条约定：

- **写命令**都支持 `--dry-run`（只预演，返回 `change_id`）、`--wait`（需要审批时等结果）、`--instruction`（用户原话，显示在审批卡片上，也可以用 `$PATATO_INSTRUCTION`）。只发送显式给出的字段。
- **价格按元输入**：`--price monthly=19.9`，CLI 按字符串换算成分发给服务端，不经过浮点数。周期键沿用 Xboard：`monthly quarterly half_yearly yearly two_yearly three_yearly onetime reset_traffic`。
- **`approval approve <id>`** 在本机浏览器打开 Admin 的确认页（`/approvals/<id>?via=cli`），人核对后用 passkey 批准，审计里记为「CLI 拉起的 passkey」；CLI 默认等到有结果再退出。
- **`install-cmd --command-only`** 只输出命令本身，便于交给 tolato：`tolato exec tokyo-01 -- "$(patato node install-cmd tokyo-03 --command-only)"`。
- **`--json`** 输出服务端返回的原始 JSON，供 Agent 解析。
- **`--actor`**（或 `$PATATO_ACTOR`）写进 `X-Actor`，例如「Claude Code 会话」；只进审计，不参与权限判断。
- 写请求自动带 `Idempotency-Key`；网络错误或服务端 5xx 时自动重试，复用同一个键，服务端保证只执行一次。

退出码：`0` 成功（包括预演、已提交审批）；`1` 出错；`2` 用法不对；`3` 变更被拒绝、过期、作废或执行失败（`--wait` 时尤其有用）。

## 代码结构

| 目录 | 内容 |
| --- | --- |
| `cli/cmd/patato` | 命令：参数解析（允许 flag 写在位置参数后面）、按显示宽度对齐的中文表格、变更计划与审批的输出 |
| `cli/internal/client` | HTTP 客户端：Bearer token、X-Actor、幂等键、重试、统一错误格式、事件流（SSE） |
| `cli/internal/config` | profile 配置的读写和选择 |

## 测试

- `cli/...` 下的单测：价格换算、参数解析、表格对齐、profile 选择、客户端重试复用幂等键。
- `cli/cmd/patato/endpoints_test.go`：CLI 用到的每个接口都必须在 `api/openapi.yaml` 里。
- `server/internal/app/cli_test.go`：编译真实的 `patato` 二进制，对进程内的服务端走一遍：设备授权登录、常规写操作、带价格的套餐走审批、预演后 `change apply --wait` 等到批准、被拒绝时退出码 3、审计里带调用方自报身份、只读 token 不能调写接口、退出登录吊销 token。

## 与计划的差异

- **没有用 oapi-codegen 生成客户端**：契约里大部分响应没有写 schema，生成的客户端只能返回原始响应，类型上帮不了多少。改为手写薄客户端，用 `endpoints_test.go` 保证路径和方法与契约一致；契约补上响应 schema 后可以换成生成的。
- **登录不是 tolato 的本地回环 + PKCE**：Patato 服务端提供的是设备授权（短码 + Admin 确认页），在没有浏览器的服务器上也能用（`--no-browser`），CLI 照此实现。
- **只读 token 不能预演**：服务端所有写接口（包括 `dry_run`）都要求可写权限。设计文档没有明确这一点，暂按服务端现状。

为配合 CLI，服务端补了两处：`GET /api/v1/auth/me` 返回 `admin_url`（CLI 据此打开审批确认页）；`POST /api/v1/auth/logout` 带 Bearer token 时吊销这个 token。

## 已知限制

- 二期的 `order`、`ticket`、`referral` 命令还没有，等对应接口。
- 发布流程还没有：目前只能 `make install-cli` 从源码安装。
