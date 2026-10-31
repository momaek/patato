import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// 开发时把 /api 转给本机的 patato-server；服务端配置里 portal_url 要写成 http://localhost:5174
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  return {
    plugins: [vue(), tailwindcss()],
    server: {
      port: 5174,
      proxy: { '/api': { target: env.PATATO_API ?? 'http://localhost:8080', changeOrigin: false } },
    },
  }
})
