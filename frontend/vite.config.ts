import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import vueI18n from '@intlify/vite-plugin-vue-i18n'
import path from 'path'

// base: './' 保证构建产物使用相对路径，适配 OpenResty 反代下的资源加载。
// outDir: 'dist' 与 Go 后端 //go:embed frontend/dist 对应。
export default defineConfig({
  base: './',
  plugins: [
    vue(),
    // 构建期预编译 i18n messages（zh/en JSON → message functions），
    // 配合 vue-i18n runtimeOnly 模式，运行时不再 new Function，满足 CSP script-src 'self'。
    vueI18n({
      include: path.resolve(__dirname, './src/i18n/locales/**'),
    }),
  ],
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    chunkSizeWarningLimit: 2000,
  },
  server: {
    port: 5173,
    proxy: {
      // 本地开发时把 API/WS 代理到后端
      '/api': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
    },
  },
})
