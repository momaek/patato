import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { QueryClient, VueQueryPlugin } from '@tanstack/vue-query'
import { isApiError } from '@patato/api'
import App from './App.vue'
import { router } from './router'
import './main.css'
import './theme'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      retry: (count, e) => !isApiError(e) || (e.status >= 500 && count < 2),
    },
  },
})

createApp(App).use(createPinia()).use(router).use(VueQueryPlugin, { queryClient }).mount('#app')
