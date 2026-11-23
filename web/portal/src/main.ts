import { createApp } from 'vue'
import { VueQueryPlugin, QueryClient } from '@tanstack/vue-query'
import { isApiError } from '@patato/api'
import App from './App.vue'
import { router } from './router'
import { i18n } from './i18n'
import './main.css'
import './theme'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: true,
      // 401 说明会话没了，重试也没用
      retry: (count, e) => !isApiError(e) || (e.status >= 500 && count < 2),
    },
  },
})

createApp(App).use(router).use(i18n).use(VueQueryPlugin, { queryClient }).mount('#app')
