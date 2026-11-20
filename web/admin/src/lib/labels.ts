import { fmt } from '@patato/api'
import type { Tone } from '@patato/ui'

export const protocols: Record<string, string> = {
  vless: 'VLESS', vmess: 'VMess', trojan: 'Trojan', shadowsocks: 'Shadowsocks', hysteria: 'Hysteria2', tuic: 'TUIC', anytls: 'AnyTLS',
}

export const backends: Record<string, string> = { native: 'patato-node', uniproxy: 'V2bX / XrayR' }

export const userStates: Record<string, { label: string; tone: Tone }> = {
  active: { label: '使用中', tone: 'ok' },
  expired: { label: '已到期', tone: 'bad' },
  over_quota: { label: '超额停用', tone: 'warn' },
  banned: { label: '已封禁', tone: 'bad' },
}

export const resetMethods: Record<string, string> = { purchase_day: '按购买日', calendar_month: '按自然月', never: '不重置' }

export const risks: Record<string, { label: string; tone: Tone }> = {
  read: { label: '只读', tone: 'neutral' },
  normal: { label: '常规', tone: 'neutral' },
  sensitive: { label: '敏感', tone: 'warn' },
  forbidden: { label: '禁止', tone: 'bad' },
}

export const entries: Record<string, string> = {
  cli: 'patato CLI', web: 'Admin 网页', telegram: 'Telegram', cli_passkey: 'CLI 拉起的 passkey', portal: '用户侧', system: '系统',
  payment: '支付回调', automation: '自动处置',
}

export const results: Record<string, { label: string; tone: Tone }> = {
  success: { label: '成功', tone: 'ok' },
  pending_approval: { label: '等待审批', tone: 'warn' },
  rejected: { label: '已拒绝', tone: 'neutral' },
  voided: { label: '已作废', tone: 'neutral' },
  failed: { label: '失败', tone: 'bad' },
  vote: { label: '已批准一票', tone: 'accent' },
}

export const approvalStates: Record<string, { label: string; tone: Tone }> = {
  pending: { label: '待审批', tone: 'warn' },
  approved: { label: '已批准', tone: 'ok' },
  rejected: { label: '已拒绝', tone: 'neutral' },
  expired: { label: '已过期', tone: 'neutral' },
  failed: { label: '执行失败', tone: 'bad' },
}

export const machineStates: Record<string, { label: string; tone: Tone }> = {
  enrolled: { label: '已接入', tone: 'ok' },
  pending_enroll: { label: '等待接入', tone: 'warn' },
  not_enrolled: { label: '未接入', tone: 'idle' },
  revoked: { label: '已吊销', tone: 'bad' },
}

export const subjectKinds: Record<string, string> = { operator: '运营者', token: 'CLI token', user: '终端用户', system: '系统', telegram: 'Telegram' }

const fieldNames: Record<string, string> = {
  name: '名称', code: '代号', address: '地址', port: '端口', protocol: '协议', region: '地区', state: '状态', backend: '节点后端',
  email: '邮箱', plan: '套餐', expires_at: '到期', transfer_gb: '流量（GB）', device_limit: '设备数', speed_limit_mbps: '限速（Mbps）',
  reset_method: '流量重置', prices: '价格', sellable: '对外售卖', banned: '封禁', rate: '倍率', tags: '标签',
  rollout_prices: '灰度价格', rollout_percent: '灰度比例', monthly_cost: '月成本（分）', status: '状态', refund_amount: '退款（分）',
  kind: '类型', value: '优惠值', max_uses: '总次数', per_user: '每人次数', enabled: '启用', rate_percent: '返佣比例',
  wait_days: '入账等待天数', first_order_only: '只返首单', subnet_limit: '同网段阈值',
  rule_profile: '分流规则', description: '说明', source_url: '模板地址', content: '模板内容', user_selectable: '允许用户自选',
  groups: '策略组', rules: '规则数', lists: '规则文件数',
}

const valueNames: Record<string, Record<string, string>> = {
  state: { pending: '待接入', online: '在线', offline: '离线' },
  backend: { native: 'patato-node', uniproxy: 'V2bX / XrayR' },
  protocol: { vless: 'VLESS', vmess: 'VMess', trojan: 'Trojan', shadowsocks: 'Shadowsocks', hysteria: 'Hysteria2', tuic: 'TUIC', anytls: 'AnyTLS' },
  reset_method: { purchase_day: '按购买日', calendar_month: '按自然月', never: '不重置' },
}

export const orderStatuses: Record<string, { label: string; tone: Tone }> = {
  pending: { label: '待支付', tone: 'idle' },
  paid: { label: '已支付', tone: 'ok' },
  refunding: { label: '退款审批中', tone: 'warn' },
  refunded: { label: '已退款', tone: 'neutral' },
  cancelled: { label: '已取消', tone: 'neutral' },
}

export const orderKinds: Record<string, string> = { new: '新购', renew: '续费', change: '换套餐', reset_traffic: '流量重置' }

export const commissionStates: Record<string, { label: string; tone: Tone }> = {
  pending: { label: '待入账', tone: 'idle' },
  frozen: { label: '已冻结', tone: 'warn' },
  settled: { label: '已入账', tone: 'ok' },
  cancelled: { label: '已作废', tone: 'neutral' },
}

export const ticketStates: Record<string, { label: string; tone: Tone }> = {
  open: { label: '待回复', tone: 'warn' },
  replied: { label: '已回复', tone: 'ok' },
  closed: { label: '已关闭', tone: 'neutral' },
}

export const couponStates: Record<string, { label: string; tone: Tone }> = {
  active: { label: '可用', tone: 'ok' },
  disabled: { label: '已停用', tone: 'neutral' },
  expired: { label: '已过期', tone: 'neutral' },
  scheduled: { label: '未开始', tone: 'idle' },
  used_up: { label: '已用完', tone: 'neutral' },
}

export const eventKinds: Record<string, string> = {
  node_offline: '节点离线', node_online: '节点恢复', user_expired: '用户到期', user_expiring: '用户即将到期', user_over_quota: '流量超额',
  panel_host_exec: '面板主机执行', traffic_anomaly: '流量异常', order_paid: '订单支付', order_refunded: '订单退款',
  order_needs_attention: '订单需人工处理', referral_frozen: '返佣冻结', ticket_created: '新工单', ticket_replied: '工单已回复',
}

export const automationActions: Record<string, string> = {
  email_user: '给用户发邮件', operation: '执行变更操作', replace_offline_node: '用备用节点替换掉线节点',
}

export const periods: Record<string, string> = {
  monthly: '月', quarterly: '季', half_yearly: '半年', yearly: '年', two_yearly: '两年', three_yearly: '三年',
  onetime: '一次性', reset_traffic: '重置流量',
}

/** 套餐价格：{monthly: 1990} → 「19.90 / 月」。 */
export function priceText(prices: Record<string, number> | null | undefined): string {
  const entries = Object.entries(prices ?? {})
  if (!entries.length) return '未定价'
  return entries.map(([k, v]) => (k === 'onetime' || k === 'reset_traffic' ? `${periods[k]} ${fmt.money(v)}` : `${fmt.money(v)} / ${periods[k] ?? k}`)).join('，')
}

/** 对象型的变更值拆成「字段名、值」逐行展示；不是对象时返回 null。 */
export function fields(v: unknown): [string, string][] | null {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return null
  return Object.entries(v as Record<string, unknown>).map(([k, x]) => [
    fieldNames[k] ?? k,
    k === 'prices' ? priceText(x as Record<string, number>) : (valueNames[k]?.[String(x)] ?? show(x)),
  ])
}

/** diff 里的字段名换成中文，没有对应时原样显示。 */
export function fieldName(k: string): string {
  return fieldNames[k] ?? k
}

const isoTime = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/

/** 把 diff 里的值换成可读文字：数组用顿号连接，空值显示「无」，时间按本地时区显示。 */
export function show(v: unknown): string {
  if (v === null || v === undefined || v === '') return '无'
  if (typeof v === 'string' && isoTime.test(v)) return fmt.date(v) + ' ' + fmt.time(v)
  if (Array.isArray(v)) return v.length ? v.map(show).join('、') : '无'
  if (typeof v === 'boolean') return v ? '是' : '否'
  if (typeof v === 'object') return Object.entries(v as Record<string, unknown>).map(([k, x]) => `${k}: ${show(x)}`).join('，')
  return String(v)
}

// ---- 中转线路 ------------------------------------------------------------

export const hopStates: Record<string, { label: string; tone: Tone }> = {
  online: { label: '在线', tone: 'ok' },
  offline: { label: '离线', tone: 'bad' },
  unknown: { label: '状态未知', tone: 'idle' },
}

export const forwardStatus: Record<string, { label: string; tone: Tone }> = {
  pending: { label: '待生效', tone: 'warn' },
  active: { label: '已生效', tone: 'ok' },
  failed: { label: '失败', tone: 'bad' },
  manual: { label: '手写规则', tone: 'neutral' },
}

export const entryKinds: Record<string, string> = { direct: '直连', custom: '自定义地址', relay: '中转' }

export const forwardModes: Record<string, string> = { auto: '自动（能用 nftables 就用）', nft: 'nftables 内核转发', userspace: '用户态（sing-box）' }

export function entryTone(e: { enabled: boolean; available: boolean }): Tone {
  if (!e.enabled) return 'idle'
  return e.available ? 'ok' : 'bad'
}
