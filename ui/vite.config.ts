import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The dev server proxies /api, /auth and /mcp to KUBE_BMC_API, e.g. a port-forwarded kube-bmc server.
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../web/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1200,
  },
  server: {
    proxy: Object.fromEntries(['/api', '/auth', '/mcp'].map((p) => [p, process.env.KUBE_BMC_API ?? 'http://127.0.0.1:8080'])),
  },
})
