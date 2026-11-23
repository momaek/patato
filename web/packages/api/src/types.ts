// 响应类型，对应 server/internal/api 里的视图结构。

export interface Ref {
  id: number
  code: string
  name: string
}

export interface List<T> {
  items: T[]
  next_cursor?: number | null
}

// ---- 运营者 ------------------------------------------------------------

export interface WhoAmI {
  kind: 'operator' | 'token'
  operator: { id: number; name: string }
  scope: string
  token_id: number
  can_approve: boolean
  admin_url?: string
}

export interface WebAuthnBegin {
  challenge_id: string
  options: { publicKey: any; mediation?: string }
}

export interface OperatorSession {
  operator: { id: number; name: string }
  expires_at: string
}

export interface CLIGrant {
  user_code: string
  client_name: string
  scope: 'read' | 'write'
  status: 'pending' | 'approved' | 'denied' | 'used' | 'expired'
  expires_at: string
}

// ---- 总览 --------------------------------------------------------------

export interface Todo {
  kind: string
  severity: 'info' | 'warning' | 'critical'
  title: string
  detail: string
  ref: string
  expires_at?: string
  at: string
}

export interface Status {
  todo: Todo[]
  nodes: { total: number; online: number; offline: number; pending: number; items: Node[] }
  users: Record<string, number>
  expiring: { today: number; days_3: number; days_7: number }
  usage: { today_bytes: number; online_users: number }
  panel_host: { status: 'ok' | 'alert' | 'unsupported' | 'error' | 'not_configured'; checked_at: string; message: string }
  tickets: { open: number; needs_human: number }
  sales: { today_revenue: number; today_orders: number }
  updated_at: string
}

// ---- 节点 --------------------------------------------------------------

export type NodeState = 'online' | 'offline' | 'pending'

export interface NodeRuntime {
  running: boolean
  error?: string
  listening?: boolean
  cert_expires_at?: string
  users: number
  connections: number
  applied_at: string
}

export interface Node {
  id: number
  code: string
  name: string
  region: string
  host: string
  address: string
  protocol: string
  backend: 'native' | 'uniproxy'
  port: number
  server_port: number
  state: NodeState
  in_subscription: boolean
  rate: number
  tags: string[]
  plans: Ref[]
  online_users: number
  today_bytes: number
  last_heartbeat_at: string | null
  last_push_at: string | null
  load?: Record<string, unknown>
  runtime?: NodeRuntime
  settings?: Record<string, unknown>
  allow_legacy_token: boolean
  monthly_cost: number
  version: number
  created_at: string
}

export interface Machine {
  host: string
  status: 'enrolled' | 'pending_enroll' | 'not_enrolled' | 'revoked'
  connected: boolean
  hostname: string
  agent_version: string
  kernel: string
  enrolled_at: string | null
  last_seen_at: string | null
  nodes: Ref[]
  network: MachineNetwork | null
}

// ---- 套餐 --------------------------------------------------------------

export interface Plan {
  id: number
  code: string
  name: string
  transfer_gb: number
  reset_method: 'purchase_day' | 'calendar_month' | 'never'
  device_limit: number
  speed_limit_mbps: number
  prices: Record<string, number>
  rollout_prices?: Record<string, number>
  rollout_percent: number
  sellable: boolean
  /** 默认分流规则代号；为空时用内置规则 */
  rule_profile: string | null
  user_count: number
  node_count: number
  nodes?: Ref[]
  version: number
  updated_at: string
}

// ---- 分流规则 ------------------------------------------------------------

export interface RuleProfile {
  id: number
  code: string
  name: string
  description: string
  /** ini 模板地址；为空时是直接粘贴的内容 */
  source_url: string
  user_selectable: boolean
  fetched_at: string | null
  fetch_error?: string
  groups: string[]
  rule_count: number
  list_count: number
  warnings?: string[]
  parse_error?: string
  plan_count?: number
  user_count?: number
  /** 只在详情里返回 */
  content?: string
  lists?: { name: string; url: string; fetched_at: string | null; fetch_error?: string }[]
  version: number
  updated_at: string
}

export interface RulePreset {
  code: string
  name: string
  description: string
  source_url: string
}

// ---- 用户 --------------------------------------------------------------

export type UserState = 'active' | 'expired' | 'over_quota' | 'banned'

export interface User {
  id: number
  email: string
  plan: Ref | null
  state: UserState
  transfer_bytes: number
  used_bytes: number
  used_up: number
  used_down: number
  expires_at: string | null
  next_reset_at: string | null
  device_limit: number | null
  speed_limit_mbps: number | null
  devices_online: number
  token_reset_at: string | null
  last_active_at: string | null
  balance: number
  invited_by: number | null
  /** 用户自选的分流规则；为空时跟随套餐 */
  rule_profile: string | null
  created_at: string
  version: number
}

export interface DailyTraffic {
  Day: string
  Up: number
  Down: number
}

export interface UserDetail {
  user: User
  recent_audit: Audit[]
  daily_traffic: DailyTraffic[] | null
}

// ---- 审计 --------------------------------------------------------------

export interface Audit {
  id: number
  at: string
  subject: string
  actor_claim: string
  entry: string
  action: string
  target: string
  risk: string
  change_id: string
  result: string
  detail: { summary?: string; error?: string; [k: string]: unknown } | null
}

// ---- 变更与审批 --------------------------------------------------------

export type Risk = 'read' | 'normal' | 'sensitive' | 'forbidden'

export interface ChangePlan {
  change_id: string
  kind: string
  summary: string
  plan: string[]
  impact: { users: number; nodes: number }
  diff: { field: string; before: unknown; after: unknown }[] | null
  targets: { type: string; id: number; version: number }[] | null
  declared_risk: Risk
  risk: Risk
  risk_reason?: string
  status: string
  expires_at: string
  approval_id?: string
  result?: unknown
  error?: string
}

export type ApprovalStatus = 'pending' | 'approved' | 'rejected' | 'expired' | 'failed'

export interface ApprovalItem {
  approval_id: string
  change_id: string
  status: ApprovalStatus
  summary: string
  kind: string
  risk: Risk
  risk_reason: string
  impact: { users: number; nodes: number }
  requester: string
  actor_claim: string
  expires_at: string
  created_at: string
}

export interface ApprovalDetail {
  approval_id: string
  status: ApprovalStatus
  expires_at: string
  created_at: string
  decided_by?: string
  decided_via?: string
  decided_at?: string
  reason?: string
  change: ChangePlan
  requester: { kind: string; label: string; actor_claim?: string; entry: string }
  instruction?: string
  required: number
  votes: { operator: string; via: string; at: string }[]
  needs_code: boolean
}

export interface PlanRevision {
  version: number
  snapshot: { name: string; transfer_gb: number; prices: Record<string, number>; rollout_prices?: Record<string, number>; rollout_percent: number; sellable: boolean }
  replaced_at: string
}

// ---- 售卖与运营（二期）---------------------------------------------------

export type OrderStatus = 'pending' | 'paid' | 'refunded' | 'cancelled'

export interface Order {
  id: number
  trade_no: string
  user_id: number
  email?: string
  plan_id: number | null
  plan_name: string
  period: string
  period_name: string
  kind: 'new' | 'renew' | 'change' | 'reset_traffic'
  amount: number
  discount: number
  balance_used: number
  total: number
  status: OrderStatus
  refund_pending?: boolean
  refund_approval_id?: string
  method: string
  gateway_trade_no?: string
  days: number
  refund_amount: number
  refund_days: number
  note?: string
  source: 'portal' | 'admin' | 'xboard'
  created_at: string
  expires_at: string
  paid_at: string | null
  cancelled_at: string | null
  refunded_at: string | null
  version: number
}

export interface OrderDetail {
  order: Order
  coupon: { id: number; code: string; name: string } | null
  commission: { id: number; inviter_id: number; amount: number; status: CommissionStatus; reason: string; release_at: string } | null
  callbacks: { id: number; method: string; verified: boolean; result: string; received_at: string }[]
  audit: Audit[]
  user: { id: number; email: string; expires_at: string | null; balance: number }
}

export interface Coupon {
  id: number
  code: string
  name: string
  kind: 'amount' | 'percent'
  value: number
  plan_ids: number[]
  periods: string[]
  max_uses: number
  per_user: number
  used: number
  starts_at: string | null
  ends_at: string | null
  enabled: boolean
  state: 'active' | 'disabled' | 'expired' | 'scheduled' | 'used_up'
  created_at: string
  version: number
}

export type CommissionStatus = 'pending' | 'frozen' | 'settled' | 'cancelled'

export interface Commission {
  id: number
  inviter_id: number
  inviter_email: string
  invitee_id: number | null
  invitee_email: string
  trade_no: string
  amount: number
  status: CommissionStatus
  reason: string
  release_at: string
  settled_at: string | null
  created_at: string
}

export interface ReferralRules {
  enabled: boolean
  rate_percent: number
  wait_days: number
  first_order_only: boolean
  subnet_limit: number
}

export interface Ticket {
  id: number
  user_id?: number
  email?: string
  subject: string
  status: 'open' | 'replied' | 'closed'
  needs_human?: boolean
  needs_human_reason?: string
  has_draft?: boolean
  created_at: string
  last_message_at: string
  closed_at: string | null
  version?: number
}

export interface TicketMessage {
  id: number
  author: 'user' | 'operator'
  operator?: string
  drafted_by?: string
  body: string
  created_at: string
}

export interface TicketDetail {
  ticket: Ticket
  messages: TicketMessage[]
  draft: { body: string; drafted_by: string; basis: string[] | null; updated_at: string } | null
  context: { user: User; balance: number; orders: Order[]; plan_nodes: { code: string; name: string; state: NodeState }[] }
}

export interface Webhook {
  id: number
  name: string
  url: string
  kinds: string[]
  enabled: boolean
  last_status: string
  last_delivery_at: string | null
  created_at: string
}

export interface AutomationRule {
  id: number
  name: string
  event: string
  action: 'email_user' | 'operation' | 'replace_offline_node'
  params: Record<string, unknown>
  enabled: boolean
  last_fired_at: string | null
  last_result: string
  created_at: string
}

export interface TrafficReport {
  days: number
  since: string
  total_bytes: number
  series: { day: string; up: number; down: number; bytes: number }[]
  top_users: { user_id: number; email: string; bytes: number }[]
}

export interface RevenueReport {
  days: number
  since: string
  revenue: number
  refunds: number
  net: number
  orders: number
  series: { day: string; orders: number; revenue: number; refunds: number; balance_used: number; discount: number }[]
  by_plan: { plan: string; orders: number; revenue: number }[]
}

export interface NodeCostReport {
  days: number
  total_cost: number
  total_bytes: number
  items: { id: number; code: string; name: string; region: string; state: NodeState; monthly_cost: number; cost: number; bytes: number; cost_per_gb: number | null; users: number }[]
}

// ---- 用户侧 ------------------------------------------------------------

export interface MySubscription {
  email: string
  plan: { code: string; name: string; reset_method: Plan['reset_method'] } | null
  state: UserState
  transfer_bytes: number
  used_bytes: number
  remaining_bytes: number
  next_reset_at: string | null
  expires_at: string | null
  days_left: number | null
  devices_online: number
  device_limit: number
  subscription_url: string
  nodes: { name: string; region: string; status: string }[]
  /** 当前生效的分流规则；为空时用内置规则 */
  rule_profile: { code: string; name: string } | null
  service_name: string
  /** 落地访问统计开启时才有：用于说明统计范围与保留期限 */
  destination_stats?: { retention_days: number } | null
}

export interface MyRuleProfile {
  code: string
  name: string
  description: string
  groups: string[]
}

export interface MyRuleProfiles {
  /** 用户自选的规则代号；为空时跟随套餐 */
  selected: string | null
  plan_default: MyRuleProfile | null
  effective: MyRuleProfile | null
  items: MyRuleProfile[]
}

export interface PortalConfig {
  service_name: string
  registration: boolean
  registration_invite: boolean
  payment: boolean
  tickets: boolean
}

export interface PlanPrice {
  period: string
  name: string
  price: number
  days: number
}

export interface MyPlans {
  items: {
    code: string
    name: string
    transfer_bytes: number
    reset_method: Plan['reset_method']
    device_limit: number
    speed_limit_mbps: number
    prices: PlanPrice[]
    current: boolean
  }[]
  methods: { code: string; name: string }[]
  balance: number
}

export interface Quote {
  plan: string
  plan_name: string
  period: string
  kind: Order['kind']
  amount: number
  discount: number
  balance_used: number
  total: number
  days: number
  coupon?: string
  notes: string[]
}

export interface MyOrder {
  trade_no: string
  plan_name: string
  period: string
  period_name: string
  kind: Order['kind']
  amount: number
  discount: number
  balance_used: number
  total: number
  status: OrderStatus
  method: string
  days: number
  refund_amount: number
  created_at: string
  expires_at: string
  paid_at: string | null
  refunded_at: string | null
}

export interface MyReferral {
  invite_code: string
  invite_url: string
  enabled: boolean
  rate_percent: number
  wait_days: number
  invited: number
  balance: number
  pending: number
  settled: number
  items: Commission[]
}

// ---- 中转线路 ------------------------------------------------------------

export interface PortRange {
  local: string
  public_from?: number
}

export interface MachineNetwork {
  public_address: string
  address: string
  port_ranges: PortRange[]
  effective_port_ranges: PortRange[]
  reserved_ports: PortRange[]
  nat: boolean
  forward_mode: 'auto' | 'nft' | 'userspace'
  tolato_ip: string
  state: 'online' | 'offline' | 'unknown'
  runtime: { mode: string; error?: string } | null
  forwards: number
  failed_forwards: number
  ports_used: number
  ports_total: number
  today_up: number
  today_down: number
  month_up: number
  month_down: number
}

export interface RelayForward {
  id: number
  hop: number
  machine: string
  machine_state: 'online' | 'offline' | 'unknown'
  listen_port: number
  public_port: number
  target_host: string
  target_ip: string
  target_port: number
  mode: 'manual' | 'nft' | 'userspace'
  status: 'pending' | 'active' | 'failed' | 'manual'
  applied_mode: string
  applied_at: string | null
  error: string
  probe_ok: boolean | null
  probe_ms: number | null
  probe_at: string | null
  probe_failures: number
  probe_success_rate_24h: number | null
  probe_avg_ms_24h: number | null
  today_up: number
  today_down: number
  /** 入口组成员上的镜像转发：与第一跳对外端口相同、转到同一个下一跳 */
  mirror: boolean
  /** 第一跳或镜像：入口地址当前解析到这台机器 */
  active?: boolean
}

export interface NodeEntry {
  id: number
  node_id: number
  node: string
  kind: 'direct' | 'custom' | 'relay'
  route: Ref | null
  name: string
  suffix: string
  host: string
  port: number
  enabled: boolean
  available: boolean
  reason?: string
  sort: number
  implicit?: boolean
  forwards: RelayForward[]
  today_up: number
  today_down: number
  version: number
}

export interface RelayHop {
  id: number
  host: string
  address: string
  state: 'online' | 'offline' | 'unknown'
  /** 入口组：入口地址当前解析到这台机器（只在第一跳和成员上） */
  active?: boolean
}

export interface RelayRoute {
  id: number
  code: string
  name: string
  hops: RelayHop[]
  /** 入口组：和第一跳一起承担入口的机器，共用 entry_address */
  entry_peers: RelayHop[]
  entry_address: string
  /** 服务端最近一次解析 entry_address 的结果 */
  entry_ips: string[]
  entry_resolved_at: string | null
  nodes: number
  entries: NodeEntry[]
  today_up: number
  today_down: number
  month_up: number
  month_down: number
  version: number
}

export interface RouteTraffic {
  days: number
  since: string
  items: { route: Ref; up: number; down: number; series: { day: string; up: number; down: number }[] }[]
}

// ---- 落地访问统计 ------------------------------------------------------

export interface DestinationItem {
  kind: 'domain' | 'ip' | 'other'
  dest: string
  up: number
  down: number
  bytes: number
  conns: number
  /** 只在 other 上：被合并的目标数 */
  merged?: number
  /** 节点视图：访问过这个目标的用户数 */
  users?: number
  /** 用户视图：经过的节点数 */
  nodes?: number
}

export interface DestinationRanking {
  enabled: boolean
  retention_days: number
  since: string
  period: string
  node?: Ref
  user?: { id: number; email: string }
  total: { up: number; down: number; bytes: number; conns: number; destinations: number }
  items: DestinationItem[]
}
