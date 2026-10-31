// 展示用的格式化：流量、数字、时间。时间统一按浏览器本地时区显示。

const nf = new Intl.NumberFormat('zh-CN')

export function num(n: number | null | undefined): string {
  return n === null || n === undefined ? '—' : nf.format(n)
}

/** 字节数换成 GB / TB 等，保留一位小数；按 1024 进制，与服务端计费一致。 */
export function bytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? v : v.toFixed(1).replace(/\.0$/, '')} ${units[i]}`
}

/** 价格以最小货币单位（分）存储，显示成元，整数不带小数。货币由运营方决定，这里不加符号。 */
export function money(cents: number | null | undefined): string {
  if (cents === null || cents === undefined) return '—'
  const v = cents / 100
  return Number.isInteger(v) ? nf.format(v) : v.toFixed(2)
}

export function gb(n: number): string {
  return `${Number.isInteger(n) ? n : n.toFixed(1)} GB`
}

function d(s: string | Date) {
  return typeof s === 'string' ? new Date(s) : s
}

function pad(n: number) {
  return String(n).padStart(2, '0')
}

export function date(s: string | Date | null | undefined): string {
  if (!s) return '—'
  const t = d(s)
  return `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())}`
}

export function time(s: string | Date | null | undefined, seconds = false): string {
  if (!s) return '—'
  const t = d(s)
  return `${pad(t.getHours())}:${pad(t.getMinutes())}` + (seconds ? `:${pad(t.getSeconds())}` : '')
}

/** 今天的只显示时间，其余显示日期和时间。 */
export function dateTime(s: string | Date | null | undefined): string {
  if (!s) return '—'
  const t = d(s)
  const now = new Date()
  if (t.toDateString() === now.toDateString()) return time(t)
  return `${date(t)} ${time(t)}`
}

export function monthDay(s: string | Date | null | undefined): string {
  if (!s) return '—'
  const t = d(s)
  return `${t.getMonth() + 1} 月 ${t.getDate()} 日`
}

/** 「12 秒前」「18 分钟前」「3 天前」。 */
export function ago(s: string | Date | null | undefined, now = new Date()): string {
  if (!s) return '—'
  const sec = Math.max(0, Math.round((now.getTime() - d(s).getTime()) / 1000))
  if (sec < 60) return `${sec} 秒前`
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`
  return `${Math.floor(sec / 86400)} 天前`
}

/** 「23 分钟后」；已过去返回「已过期」。 */
export function until(s: string | Date | null | undefined, now = new Date()): string {
  if (!s) return '—'
  const sec = Math.round((d(s).getTime() - now.getTime()) / 1000)
  if (sec <= 0) return '已过期'
  if (sec < 60) return `${sec} 秒后`
  if (sec < 3600) return `${Math.ceil(sec / 60)} 分钟后`
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时后`
  return `${Math.floor(sec / 86400)} 天后`
}

/** 邮箱脱敏：al***@example.com。 */
export function maskEmail(email: string): string {
  const [name, domain] = email.split('@')
  if (!domain) return email
  return (name.length <= 2 ? name[0] + '*' : name.slice(0, 2) + '***') + '@' + domain
}

/** 订阅链接里的 token 只露出头尾。 */
export function maskToken(url: string): string {
  return url.replace(/([0-9a-f]{4})[0-9a-f]{8,}([0-9a-f]{4})/i, '$1••••••••$2')
}
