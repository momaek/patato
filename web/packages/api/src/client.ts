// API 客户端：路径受 api/openapi.yaml 生成的类型约束，接口在契约里改了名或删掉，类型检查就会失败。
// 契约里大部分响应没有写 schema，响应类型在 types.ts 里按服务端的视图结构手写。
import type { paths } from './schema'

type Method = 'get' | 'post' | 'put' | 'patch' | 'delete'

/** 契约里支持某个方法的路径。 */
export type PathOf<M extends Method> = {
  [P in keyof paths]: [NonNullable<paths[P][M]>] extends [never] ? never : P
}[keyof paths]

export interface RequestOptions {
  /** 路径参数，替换 {id} 这类占位。 */
  params?: Record<string, string | number>
  query?: Record<string, string | number | boolean | undefined | null>
  body?: unknown
  signal?: AbortSignal
}

/** 服务端统一的错误格式：{error: {code, message, hint, retryable}}。 */
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public hint?: string,
    public retryable = false,
  ) {
    super(message)
  }
}

let base = ''

/** 设置 API 地址；默认同源（开发时由 Vite 代理，线上由 Caddy 反代 /api）。 */
export function setBaseURL(url: string) {
  base = url.replace(/\/$/, '')
}

function idempotencyKey() {
  return 'idem_' + crypto.randomUUID().replace(/-/g, '')
}

function buildURL(path: string, opts: RequestOptions) {
  let p = path
  for (const [k, v] of Object.entries(opts.params ?? {})) {
    p = p.replace(`{${k}}`, encodeURIComponent(String(v)))
  }
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(opts.query ?? {})) {
    if (v !== undefined && v !== null && v !== '') q.set(k, String(v))
  }
  const qs = q.toString()
  return base + p + (qs ? '?' + qs : '')
}

async function request<T>(method: Method, path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  // 管理接口的写请求要求幂等键；每次调用生成新的，重试由调用方决定
  if (method !== 'get') headers['Idempotency-Key'] = idempotencyKey()
  const res = await fetch(buildURL(path, opts), {
    method: method.toUpperCase(),
    headers,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    credentials: 'include',
    signal: opts.signal,
  })
  const text = await res.text()
  let data: any = undefined
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    const e = data?.error
    throw new ApiError(res.status, e?.code ?? 'http_' + res.status, e?.message ?? `请求失败（${res.status}）`, e?.hint, e?.retryable ?? res.status >= 500)
  }
  return data as T
}

export const api = {
  get: <T>(path: PathOf<'get'>, opts?: RequestOptions) => request<T>('get', path, opts),
  post: <T>(path: PathOf<'post'>, opts?: RequestOptions) => request<T>('post', path, opts),
  put: <T>(path: PathOf<'put'>, opts?: RequestOptions) => request<T>('put', path, opts),
  patch: <T>(path: PathOf<'patch'>, opts?: RequestOptions) => request<T>('patch', path, opts),
  delete: <T>(path: PathOf<'delete'>, opts?: RequestOptions) => request<T>('delete', path, opts),
}

export function isApiError(e: unknown, code?: string): e is ApiError {
  return e instanceof ApiError && (code === undefined || e.code === code)
}

/** 给人看的错误文字：消息加提示。 */
export function errorText(e: unknown): string {
  if (e instanceof ApiError) return e.hint ? `${e.message}。${e.hint}` : e.message
  if (e instanceof Error) return e.message === 'Failed to fetch' ? '连不上服务器，请检查网络后重试' : e.message
  return String(e)
}
