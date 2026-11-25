import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// 开发时把 /api 转给本机的 patato-server；服务端配置里 admin_url 要写成 http://localhost:5173，
// webauthn.rp_id 写 localhost
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  return {
    // 相对路径构建：服务端在 index.html 里注入 <base href>，Admin 可以挂在独立域名的根下，
    // 也可以挂在用户侧域名的隐藏路径下（admin_url: https://example.com/a8f3k2c9）
    base: './',
    plugins: [vue(), tailwindcss()],
    server: {
      port: 5173,
      proxy: { '/api': { target: env.PATATO_API ?? 'http://localhost:8080', changeOrigin: false } },
    },
  }
})
