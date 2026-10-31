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
}

export const results: Record<string, { label: string; tone: Tone }> = {
  success: { label: '成功', tone: 'ok' },
  pending_approval: { label: '等待审批', tone: 'warn' },
  rejected: { label: '已拒绝', tone: 'neutral' },
  voided: { label: '已作废', tone: 'neutral' },
  failed: { label: '失败', tone: 'bad' },
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
}

const valueNames: Record<string, Record<string, string>> = {
  state: { pending: '待接入', online: '在线', offline: '离线' },
  backend: { native: 'patato-node', uniproxy: 'V2bX / XrayR' },
  protocol: { vless: 'VLESS', vmess: 'VMess', trojan: 'Trojan', shadowsocks: 'Shadowsocks', hysteria: 'Hysteria2', tuic: 'TUIC', anytls: 'AnyTLS' },
  reset_method: { purchase_day: '按购买日', calendar_month: '按自然月', never: '不重置' },
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

/** 把 diff 里的值换成可读文字：数组用顿号连接，空值显示「无」。 */
export function show(v: unknown): string {
  if (v === null || v === undefined || v === '') return '无'
  if (Array.isArray(v)) return v.length ? v.map(show).join('、') : '无'
  if (typeof v === 'boolean') return v ? '是' : '否'
  if (typeof v === 'object') return Object.entries(v as Record<string, unknown>).map(([k, x]) => `${k}: ${show(x)}`).join('，')
  return String(v)
}
