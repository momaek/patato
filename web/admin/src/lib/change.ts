// Admin 的手动操作：与 CLI 一样走变更计划。先预演（dry_run）给人看，确认后按 change_id 执行；
// 敏感操作执行后返回审批，由人在审批页用 passkey 批准。Admin 只是 API 的客户端，不存在只有网页能做的操作。
import { api, type ChangePlan, type PathOf } from '@patato/api'

type WriteMethod = 'post' | 'put' | 'patch' | 'delete'

export interface ChangeRequest {
  method: WriteMethod
  path: string
  params?: Record<string, string | number>
  body?: Record<string, unknown>
}

function send(r: ChangeRequest, body: Record<string, unknown>) {
  const opts = { params: r.params, body }
  switch (r.method) {
    case 'post':
      return api.post<ChangePlan>(r.path as PathOf<'post'>, opts)
    case 'put':
      return api.put<ChangePlan>(r.path as PathOf<'put'>, opts)
    case 'patch':
      return api.patch<ChangePlan>(r.path as PathOf<'patch'>, opts)
    case 'delete':
      return api.delete<ChangePlan>(r.path as PathOf<'delete'>, opts)
  }
}

/** 预演：返回将要执行的计划，不改动任何数据。 */
export function preview(r: ChangeRequest): Promise<ChangePlan> {
  return send(r, { ...(r.body ?? {}), dry_run: true })
}

/** 执行预演好的计划。敏感计划返回 status = pending_approval 和 approval_id。 */
export function apply(changeID: string): Promise<ChangePlan> {
  return api.post<ChangePlan>('/api/v1/changes/{id}:apply', { params: { id: changeID } })
}

/** 元换成分；空串返回 undefined。 */
export function cents(yuan: string): number | undefined {
  const s = yuan.trim()
  if (!s) return undefined
  const v = Number(s)
  if (!Number.isFinite(v) || v < 0) throw new Error(`金额「${yuan}」不对`)
  return Math.round(v * 100)
}

export const periodKeys = ['monthly', 'quarterly', 'half_yearly', 'yearly', 'two_yearly', 'three_yearly', 'onetime', 'reset_traffic'] as const
