import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, isApiError, type WhoAmI } from '@patato/api'

/** 当前运营者。网页只接受运营者会话（passkey 登录），审批也只认会话。 */
export const useSession = defineStore('session', () => {
  const me = ref<WhoAmI | null>(null)
  const checked = ref(false)

  async function load(): Promise<WhoAmI | null> {
    try {
      me.value = await api.get<WhoAmI>('/api/v1/auth/me')
    } catch (e) {
      if (!isApiError(e) || e.status !== 401) throw e
      me.value = null
    }
    checked.value = true
    return me.value
  }

  async function logout() {
    await api.post('/api/v1/auth/logout').catch(() => {})
    me.value = null
  }

  return { me, checked, load, logout }
})
