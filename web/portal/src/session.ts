import { computed } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { useRouter } from 'vue-router'
import { api, isApiError, type MySubscription, type PortalConfig } from '@patato/api'

export const serviceNameFallback = import.meta.env.VITE_SERVICE_NAME ?? ''
export const supportContact = import.meta.env.VITE_SUPPORT_CONTACT ?? ''

/** 我的订阅；没有登录时跳到登录页。 */
export function useSubscription() {
  const router = useRouter()
  const q = useQuery({
    queryKey: ['me', 'subscription'],
    queryFn: async () => {
      try {
        return await api.get<MySubscription>('/api/v1/me/subscription')
      } catch (e) {
        if (isApiError(e) && e.status === 401) {
          router.replace({ name: 'login', query: { next: router.currentRoute.value.fullPath } })
        }
        throw e
      }
    },
    refetchInterval: 60_000,
  })
  const serviceName = computed(() => q.data.value?.service_name || serviceNameFallback)
  return { ...q, serviceName }
}

export function useLogout() {
  const router = useRouter()
  const qc = useQueryClient()
  return async () => {
    try {
      await api.delete('/api/v1/me/session')
    } finally {
      qc.clear()
      router.replace({ name: 'login' })
    }
  }
}

/** 登录前就要知道的设置：是否开放注册、是否开通在线支付。 */
export function usePortalConfig() {
  return useQuery({ queryKey: ['config'], queryFn: () => api.get<PortalConfig>('/api/v1/me/config'), staleTime: 5 * 60_000 })
}

/** 登录后的接口遇到 401 时回到登录页。 */
export function useAuthed<T>(key: unknown[], fn: () => Promise<T>, opts: { refetchInterval?: number | ((q: any) => number | false) } = {}) {
  const router = useRouter()
  return useQuery({
    queryKey: key,
    queryFn: async () => {
      try {
        return await fn()
      } catch (e) {
        if (isApiError(e) && e.status === 401) router.replace({ name: 'login', query: { next: router.currentRoute.value.fullPath } })
        throw e
      }
    },
    ...opts,
  })
}
