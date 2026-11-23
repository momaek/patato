import { createApp } from 'vue'
import { VueQueryPlugin, QueryClient } from '@tanstack/vue-query'
import { isApiError } from '@patato/api'
import { initTheme } from '@patato/ui'
import App from './App.vue'
import { router } from './router'
import { i18n } from './i18n'
import './main.css'

// 颜色与 main.css 的 --color-bg（浅色 / 深色）一致
initTheme({ themeColor: { light: '#f5f7fb', dark: '#0b1120' } })

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
