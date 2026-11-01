// passkey：登录、凭邀请注册、添加新 passkey、审批时的再次验证。
// 服务端用 go-webauthn，begin 返回 {challenge_id, options: {publicKey}}，finish 收浏览器给出的凭证 JSON。
import { startAuthentication, startRegistration, browserSupportsWebAuthn } from '@simplewebauthn/browser'
import { api, type OperatorSession, type WebAuthnBegin, type ApprovalDetail } from '@patato/api'

export const supported = () => browserSupportsWebAuthn()

export async function login(): Promise<OperatorSession> {
  const b = await api.post<WebAuthnBegin>('/api/v1/auth/passkey/login:begin')
  const credential = await startAuthentication({ optionsJSON: b.options.publicKey })
  return api.post<OperatorSession>('/api/v1/auth/passkey/login:finish', { body: { challenge_id: b.challenge_id, credential } })
}

export async function register(invite: string): Promise<OperatorSession> {
  const b = await api.post<WebAuthnBegin>('/api/v1/auth/passkey/register:begin', { body: { invite } })
  const credential = await startRegistration({ optionsJSON: b.options.publicKey })
  return api.post<OperatorSession>('/api/v1/auth/passkey/register:finish', { body: { invite, challenge_id: b.challenge_id, credential } })
}

export async function addPasskey(): Promise<void> {
  const b = await api.post<WebAuthnBegin>('/api/v1/auth/passkeys:begin')
  const credential = await startRegistration({ optionsJSON: b.options.publicKey })
  await api.post('/api/v1/auth/passkeys:finish', { body: { challenge_id: b.challenge_id, credential } })
}

/** 批准审批：先为这一条审批要一次 passkey 验证，再提交。via 为 cli_passkey 表示由 CLI 拉起。 */
export async function approve(id: string, via: 'web' | 'cli_passkey', code?: string): Promise<ApprovalDetail> {
  const b = await api.post<WebAuthnBegin>('/api/v1/approvals/{id}:approve-begin', { params: { id } })
  const credential = await startAuthentication({ optionsJSON: b.options.publicKey })
  return api.post<ApprovalDetail>('/api/v1/approvals/{id}:approve', { params: { id }, body: { challenge_id: b.challenge_id, credential, via, code } })
}

/** 用户取消或超时都会抛 NotAllowedError，给一句人话。 */
export function passkeyError(e: unknown): string | null {
  if (e instanceof Error && (e.name === 'NotAllowedError' || e.name === 'AbortError')) return '已取消 passkey 验证，或验证超时'
  return null
}
