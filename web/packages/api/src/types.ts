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
  sellable: boolean
  user_count: number
  node_count: number
  nodes?: Ref[]
  version: number
  updated_at: string
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
  service_name: string
}
