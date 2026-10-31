import { computed } from 'vue'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { useRouter } from 'vue-router'
import { api, isApiError, type MySubscription } from '@patato/api'

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
