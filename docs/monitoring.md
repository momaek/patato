# 中转线路与落地访问监控 · 开发计划

> 2026-11-20 · 依据 [设计文档](design.md)、[开发计划](plan.md) 与现网中转的只读勘察。线路、入口与托管转发的详细设计（端口分配、nftables 规则、下发协议、风险等级、迁移步骤）见 [中转线路设计](relay.md)。

## 目标

1. **中转线路流量**：看到每条中转线路（入口 → 若干中转 → 落地）今天、本月走了多少流量，哪一跳出了问题。
2. **落地访问统计**：看到每个落地节点（以及每个用户）访问最多的网站和 IP，用于排查流量异常、调整分流规则。
3. **不手写转发规则**：中转规则由 Patato 生成、下发和清理，运营者只描述「这条线路经过哪几台机器」。

第 3 条是第 1 条的前提：现有手写规则无法按线路区分流量（见下文「现状」），只有每条线路一条规则时才能分别计数。

## 现状

现网使用 Xboard，中转以「父节点隐藏、子节点显示」表示，中间经过哪些机器没有记录在任何地方。勘察到两类链路（已脱敏）：

| 类型 | 链路 | 转发方式 |
| --- | --- | --- |
| SS，8 条 | 客户端 → 入口机 A（广州）→ 中转机 B（香港）→ 落地 | A：一条 iptables 规则把 60000–62000 整段原端口 DNAT 到 B；B：每个端口一个 socat 服务，转到对应落地（其中一条按域名转发到动态 IP 落地，靠一个 DNS 刷新脚本维持） |
| VLESS Reality，6 条 | 客户端 → 入口机 C（香港）→ 落地 | C：每个端口一组 socat |

由此得出的约束：

- **A 上的计数器不能当流量用**：nat 链只处理每个连接的首包，A 上那条规则的计数是「连接数 × 首包大小」，平均每包约 63 字节，不是转发字节数。按字节计数必须在 forward 链或 conntrack 上统计。
- **整段 DNAT 分不出线路**：所有线路共用一条规则，只能按线路各建一条规则后才能分别计数。
- **socat 没有计数，且每个连接 fork 一个进程**：B 只有约 450 MB 内存，勘察时已有约 400 个 socat 进程。
- **落地节点后端**：现网落地跑的是 xboard-node，不能直接连 Patato（启动时依赖 `/api/v2/server/handshake`）。迁移时换成 patato-node，本计划的落地统计也只在 patato-node 上实现，V2bX / XrayR 节点不提供。

## 概念与数据模型

| 概念 | 含义 | 说明 |
| --- | --- | --- |
| 节点（已有） | 落地上的一个代理入站 | 用户身份、计费、限速、设备数都在这里；在线状态来自落地机器的心跳 |
| 线路 `route` | 有序的中转机器列表，如「广州 → 香港」 | 可被多个入口复用；机器即 Patato 中接入了 patato-node 的 `machines` |
| 入口 `entry` | 节点的一个对外地址 | 直连入口没有线路；中转入口绑定一条线路。订阅按启用的入口展开，名称为「节点名 · 后缀」 |
| 转发 `forward` | 入口在某一跳上的一条规则 | `监听端口 → 下一跳地址:端口`，TCP 与 UDP 同时转发；由 Patato 分配端口、生成并下发 |

规则：

1. **订阅只下发第一跳**的地址和端口；节点离线或任一跳不可用时，这个入口不下发（节点所有入口都不可用时沿用现有回退：下发离线节点，避免订阅清空）。
2. **入口可用 = 节点在线 ∧ 每一跳机器在线 ∧ 每一跳转发已生效**（第三阶段再加上探测结果）。
3. **端口由 Patato 分配，范围按机器配置**（见下文「机器的网络属性」）：在该机器的可分配端口段内选择，与机器上已有节点的监听端口、已分配的转发端口都不冲突。迁移期新线路使用新端口，不抢占手写规则的端口。
4. **下一跳地址**：中间跳指向下一台中转机器的「对外地址」；最后一跳指向节点所在机器的对外地址和节点的对外端口。地址变化（动态 IP 落地）时重新下发，不依赖 DNS。

### 机器的网络属性

中转机器与落地机器都可能是 NAT 机器：本机网卡上只有内网地址，商家只映射了一段外网端口，外网端口和本机端口不一定相同。勘察中已有一台落地属于这种情况（网卡地址为 `10.x`，外网经商家的域名与动态 IP 访问）；三台中转机器网卡上都是公网地址。

每台机器（`machines`）增加以下属性，默认值适用于独享公网 IP 的机器，NAT 机器在 Admin 机器页或 CLI 里填写：

| 属性 | 默认 | 说明 |
| --- | --- | --- |
| `public_address` | tolato 上报的 IP | 别的机器和客户端连它用的地址；NAT 机器填商家给的域名或外网 IP。域名由服务端解析后下发，定期重新解析 |
| `port_ranges` | `[{local: 40000-49999}]` | 可分配的端口段，可以有多段。每段写明本机端口；NAT 映射时另写外网端口起点，例如 `{local: 20000-20099, public_from: 30000}` 表示外网 30000 对应本机 20000 |
| `nat` | `false` | 为 `true` 时 `port_ranges` 必填；Patato 只在这些端口段内分配，不会使用默认段 |

使用规则：

- 转发规则监听本机端口；订阅和上一跳的目标都用 `public_address` 加外网端口。
- 落地节点同样适用：节点的监听端口（`server_port`）取本机端口，节点的对外端口（`port`）取外网端口。给 NAT 落地新建节点时，端口从它的端口段里分配。
- 端口段用尽时，变更计划的 dry_run 直接返回 `port_exhausted`，指出哪台机器、哪一段。
- Patato 无法验证商家的端口映射是否真的生效，阶段 3 的探测会发现映射不通的端口。
5. **节点的对外端口与监听端口分开**：修复现有修改节点时把 `port` 和 `server_port` 一起覆盖的问题（`server/internal/ops/nodes.go`）；TLS 默认 SNI 不再取入口地址。

新增表（goose 迁移 `00005_relays.sql`）：

- `machines` 增加列：`public_address`、`port_ranges jsonb`、`nat`（见上文「机器的网络属性」）。
- `routes`：`id`、`code`、`name`、`hops bigint[]`（`machines.id`，有序）、`version`。
- `node_entries`：`id`、`node_id`、`route_id`（可空，空为直连）、`suffix`、`enabled`、`sort`、`version`。
- `forwards`：`id`、`entry_id`、`hop`（序号）、`machine_id`、`listen_port`、`target_host`、`target_port`、`mode`（`nft` / `userspace`）、`applied_at`、`error`；`UNIQUE (machine_id, listen_port)`。
- `forward_traffic_hourly`：`forward_id`、`hour`、`up`、`down`，主键 `(forward_id, hour)`。
- `forward_traffic_daily`：同上按天，用于月度汇总与长期保留。
- `destination_stats_hourly`：`node_id`、`user_id`、`hour`、`kind`（`domain` / `ip`）、`dest`、`up`、`down`、`conns`，主键 `(node_id, user_id, hour, kind, dest)`。节点级榜单是按用户求和的结果，不单独存储。

所有写操作照常走变更计划：建线路、给节点加中转入口、启停入口都有 dry_run、impact 与审计；影响面按入口所在套餐的用户数计算风险等级。

## 实施阶段

### 阶段 1：线路与入口（只记录，不托管）

目的：支撑 Xboard 迁移，让订阅效果与现网一致，并先有每跳的在线监控。

- 数据模型与迁移；`node_entries` 替代「节点只有一个地址」，没有入口的节点等价于一个启用的直连入口。
- 订阅生成（通用、Clash / Mihomo、sing-box、Surge、Quantumult X、Loon）按入口展开。
- 手写中转登记为 `mode = manual` 的转发：只记录端口和下一跳，不下发、不计流量。
- Xboard 导入：每个子节点变成父节点的中转入口；父节点 `show = 0` 时直连入口默认停用。中间跳 Xboard 里没有，导入后由「链路发现」补齐：从入口机器开始，经 tolato 只读读取 nftables / iptables 和 socat 进程，逐跳追到落地节点，生成跳列表交给运营者确认。
- 可用性：每跳机器离线时入口不下发，并发 `route_hop_offline` 事件（Telegram / Webhook），消息里列出受影响的入口数。
- Admin：节点详情页增加「入口」区块；新增「线路」页，列出线路、跳、各入口状态。
- 契约：`/api/v1/routes`、`/api/v1/nodes/{id}/entries`，以及对应的 CLI 命令 `patato route list|create`、`patato node entry add|enable|disable`。

关口：Xboard 测试副本导入后，每个用户的订阅内容（入口地址、端口、名称）与 Xboard 生成的逐条一致；任一中转机器停机后，经过它的入口在一个心跳周期内从订阅中消失并告警。

### 阶段 2：托管转发与线路流量

目的：转发规则由 Patato 生成，按线路统计字节数。

**patato-node 中转模式**

- `SyncResponse` 增加 `forwards`（本机应有的完整转发列表与版本），与节点同步走同一 WebSocket 推送和轮询。
- 默认 `nft` 模式：patato-node 用 netlink（`github.com/google/nftables`，不依赖 `nft` 命令）独占维护 `table inet patato`，每次按完整列表原子替换：
  - `prerouting`（`dstnat` 优先级）：`tcp/udp dport <listen_port> ct mark set <forward_id> dnat to <target>`；
  - `postrouting`（`srcnat` 优先级）：`ct mark <forward_id> masquerade`；
  - `forward`（`filter` 优先级）：`ct mark <forward_id> ct direction original counter name f<id>_up`、`ct direction reply counter name f<id>_down`，按字节统计双向流量。
  - 只增删自己的表，不读写其他表和 iptables 规则；不修改 sysctl。启动时检查 `net.ipv4.ip_forward`（及 IPv6 对应项），未开启时转发报错并上报原因，由运营者用 tolato 开启。
- `userspace` 回退：机器没有 nftables、内核不支持或运营者指定时，用 sing-box `direct` 入站（`override_address` / `override_port`，TCP 与 UDP 各一个）转发。入站标签用 `r<forward_id>`，与节点的 `n<node_id>` 区分，计量层不把它当作用户连接拒绝。字节计数使用 sing 的计数回调，保留 Linux 上的 `splice` 零拷贝路径（实现时以基准测试确认）。
- 上报：`NodeReport` 之外增加 `ForwardReport{id, up, down, applied, error}`，进入现有 spool，序号去重与重试沿用。计数器按差值上报；读数变小（重启、表重建）视为归零，从新读数开始累计。
- 下一跳是域名时由服务端解析成 IP 后下发；落地机器 IP 以 tolato 上报为准，变化时重新下发。

**服务端**

- 端口分配：在变更计划里完成，dry_run 返回将要占用的每跳端口；分配与机器上已有节点端口、已有转发端口在同一事务里校验。
- 流量聚合：River 周期任务把上报写进 `forward_traffic_hourly`，每天汇总到 `forward_traffic_daily`；小时表保留 30 天，天表长期保留。
- Admin「线路」页：每条线路今天 / 本月流量、近 7 天曲线、每跳状态与最近应用时间；机器页显示该机器上转发的总流量。
- CLI：`patato route traffic [--since 7d]`、`patato route apply <route> --plan <套餐>`（把套餐内所有节点经这条线路各开一个入口）。

**从手写规则切换**

1. 在中转机器上装 patato-node（走现有 tolato-agent 接入）。
2. 为现有 `manual` 入口创建同线路的托管入口，使用新端口；订阅同时下发新旧两个入口，确认新入口可用。
3. 停用旧入口；客户端更新订阅、旧端口连续 24 小时没有新连接后，删除对应的手写规则：停用并删除 socat 的 systemd 服务（如 `aurora@<端口>`、`aurora-dns-guard`）、删除对应的 iptables 规则，并保存规则使其重启后不再恢复。由 Agent 经 tolato 执行，执行前列出要删除的服务与规则，运营者确认一次后按机器逐台删除，每台删完检查端口已不再监听、Patato 的转发仍有流量。
4. Patato 的 patato-node 自身不删除它没有创建的规则；上述删除是一次性的迁移操作，记入审计。

关口：一条两跳线路在真实机器上用 sing-box 客户端跑通 TCP 与 UDP；下载固定大小载荷后，入口机与中转机上报的字节数和落地节点记录的用户流量偏差小于 5%（差值来自协议与 TLS 开销）；删除入口后两跳上的规则在一个同步周期内消失；重启 patato-node 后流量不重复计算。

### 阶段 3：链路探测与告警

- 每台中转机器的 patato-node 每分钟对每条转发的下一跳做一次 TCP 建连，上报成功与否和耗时；UDP 没有通用探测方式，只看流量与状态。
- 入口可用性加入探测结果：连续三次失败视为不可用，从订阅中撤下；恢复后重新下发。撤下前同样检查「所有入口都不可用时不清空订阅」。
- 新增事件：`route_probe_failed`、`forward_apply_failed`（端口被占用、ip_forward 未开启等）、`route_idle`（落地节点有该入口相关流量、但线路连续 N 小时为零，可能是旁路或计数异常）。
- Admin 线路页显示每跳最近 24 小时的探测成功率和延迟。

关口：在中转机器上临时丢弃到下一跳的流量，三分钟内入口撤下并收到告警；恢复后自动重新下发。

### 阶段 4：落地访问统计

按「节点 × 用户 × 目标」采集，同时提供两种视图：节点视图（这个节点上大家访问最多的网站）由用户数据求和得到；用户视图（某个用户访问最多的网站）直接查询。保留 7 天。

**采集（patato-node）**

- 路由规则增加 `{"action": "sniff"}`（sing-box 1.13 的规则动作），超时 300 ms，用 TLS SNI、HTTP Host、QUIC 读出域名；读不到时使用连接目标本身（域名或 IP）。
- 目标归一化：域名取可注册域名（`golang.org/x/net/publicsuffix`，例如 `www.google.com` 记为 `google.com`）；IP 原样记录。
- 计量层的 `Session` 增加目标键：TCP 连接在建立时确定目标，读写时在用户计数之外再累加一次目标计数；UDP 按每个包的目标地址累加。
- 内存上限：每个节点每个上报周期最多保留 5000 个「用户 × 目标」条目，超出的合并为 `其他`，并在上报里标记被合并的条目数。
- 上报周期 5 分钟，走现有 spool；只上报聚合结果，不落连接级日志。
- 实现说明（已落地）：
  - 嗅探规则在内核启动时就加在路由规则最前，条件是 `clash_mode = patato-destinations` 且连接带用户（`auth_user` 非空，即只对 `n<id>` 用户入站生效，`r<id>` 中转入站不受影响）。patato-node 在 sing-box 上下文里注册一个只提供模式的最小 `ClashServer`（不监听 Clash API），开关落地统计就是切换模式，不重建内核、不断开连接。已确认 sing-box 1.13.21 支持 `sniff` 规则动作与 `sniffer` / `timeout` 字段。
  - 「其他」按用户合并（每个用户一条 `kind = other`），被合并的目标数记在该条的 `merged`；表 `destination_stats_hourly` 为此多一列 `merged`。
  - 关闭后服务端也不再入账：节点还没同步到关闭时上报的统计直接丢弃。
  - 按批次的上报时间（`ReportRequest.at`）归入小时；节点积压后补发的批次落在原来的小时，清理照常。

**服务端与界面**

- 写入 `destination_stats_hourly`；按配置的保留天数由周期任务删除。
- Admin 节点页「访问目标」：按时间段查看前 50 个域名 / IP 的流量和连接数，可按用户筛选；用户页有同样的榜单，可按节点筛选。
- CLI：`patato node destinations <节点> [--user <用户>] [--since 24h]`，只读接口，`x-risk: read`。
- 用户侧：在「我的订阅」页说明统计范围与保留期限。

**配置**

```yaml
monitoring:
  destinations:
    enabled: false     # 打开后所有 patato-node 落地开始采集
    retention_days: 7
```

关口：在测试用户上访问一组已知网站后，榜单中的域名与字节数和客户端侧记录一致（偏差小于 5%）；关闭配置后节点在一个同步周期内停止采集，服务端不再有新数据。

## 契约与代码改动范围

| 位置 | 改动 |
| --- | --- |
| `api/openapi.yaml` | 线路、入口、转发流量、访问目标接口；风险等级写在 `x-risk` |
| `nodeapi/nodeapi.go` | `SyncResponse.Forwards`、`ForwardReport`、`NodeReport.Destinations` |
| `node/internal/` | 新增 `relay`（nft 与 userspace 两种实现）；`kernel` 增加 `r<id>` 入站与 sniff 规则；`meter` 增加目标计数；`agent` 合并上报 |
| `server/internal/` | `store` 迁移与查询；`ops` 线路与入口的变更计划和端口分配；`subscribe` 按入口展开；`xboard` 导入入口；`jobs` 聚合、清理与探测告警；`native` 下发转发 |
| `web/admin` | 线路页、节点页入口区块与访问目标、机器页的网络属性（对外地址、端口段、NAT）与转发流量 |
| `cli` | `route`、`node entry`、`node destinations` 命令 |

## 验证

- 单元测试：nft 规则生成（与预期规则集逐条对比）、端口分配冲突、计数器归零处理、目标归一化、条目上限合并。
- 集成测试（真实 PostgreSQL）：入口展开后的订阅、转发同步的版本与去重、流量聚合、保留期清理、变更计划 dry_run 与实际执行一致。
- Linux 集成测试：在网络命名空间里起两跳转发，验证 nft 模式与 userspace 模式的 TCP / UDP 连通和字节计数；需要 root，CI 中以特权容器运行，本地用构建标签隔离。
- 真机验收：沿用 [真机核心验收](acceptance-2026-11-04.md) 的方法，加上各阶段关口，结论按同样标准记录，失败如实保留。

## 风险

- **与现有防火墙规则共存**：入口机上已有 iptables-nft 规则。Patato 的表只用自己分配的端口，且分配时避开机器上已监听或已被现有规则占用的端口（链路发现阶段读取）；切换前需逐台核对。
- **云厂商安全组**：新端口需要在安全组放行，Patato 管不到；阶段 3 的探测能发现不通，但首次开线路仍需运营者确认。
- **用户态回退的性能**：UDP 每个包都过一次用户态；以基准测试给出单核可承载的速率，写进文档。
- **动态 IP 落地**：IP 变化到重新下发之间有一个 tolato 上报周期的中断。
- **隐私**：落地统计记录的是每个用户的访问去向（主域名或 IP，不含路径），默认关闭，保留 7 天，开启时在用户侧页面告知。
- **NAT 机器的端口映射**：外网端口与本机端口的对应关系由运营者填写，填错会导致入口不通；阶段 3 的探测能发现，但无法自动纠正。
- **删除手写规则**：删错会立刻断开仍在使用的线路。只在新入口确认可用、旧端口 24 小时无新连接后删除，且逐台进行。
- **计数口径**：线路流量按线上字节统计，落地按用户收发数据统计，两者天然有几个百分点的差，报表中分开展示，不互相校正。

## 已定

| 事项 | 结论 |
| --- | --- |
| 落地统计粒度 | 按用户采集，节点视图与用户视图都提供 |
| 访问统计保留天数 | 7 天 |
| 端口段 | 按机器配置，支持多段与 NAT 端口映射；未配置时默认 `40000–49999` |
| 现有手写规则 | 切换到托管转发后直接删除，由 Agent 经 tolato 执行，运营者确认一次 |

## 待定

| 事项 | 影响 | 最晚 |
| --- | --- | --- |
| 每台 NAT 机器的外网地址与端口映射 | 阶段 2 在这些机器上分配端口。已实现的默认：NAT 机器不填端口段时不分配（返回 `port_exhausted`），由运营者用 `patato machine set` 填写 | 阶段 2 开始前 |
| ct mark 取值范围是否避让机器上的其他程序 | nft 模式暂用转发 ID 作为 mark | 首台有其他 ct mark 使用者的机器接入前 |
| 访问统计：「其他」按用户合并还是整节点合并（暂定按用户，节点视图再求和） | 用户视图能否看到自己被合并的流量 | 阶段 4 真机验收前 |
| 访问统计：UDP 会话（非 QUIC）的连接数口径（暂定同一会话内首次出现的目标算一次） | 榜单「连接」列的含义 | 阶段 4 真机验收前 |
| 访问统计：保留天数上限（暂定 1–90 天，接口的 `since` 不超过保留天数） | 配置校验 | 阶段 4 上线前 |
| 访问统计：用户侧告知文案与位置（暂定「我的订阅」页右栏卡片，开启时才显示） | 隐私告知是否足够 | 开启前 |
