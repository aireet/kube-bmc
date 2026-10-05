import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// `npm run dev` proxies the API to a local `kube-bmc server --demo` (or a port-forwarded server).
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../web/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1200,
  },
  server: {
    proxy: { '/api': process.env.KUBE_BMC_API ?? 'http://127.0.0.1:8080' },
  },
})
