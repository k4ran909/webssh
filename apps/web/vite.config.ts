import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Dev: Vite on :5173 proxies the gateway on :7022. Keeping the Host header
// unchanged (no changeOrigin) makes the WebSocket same-origin for the gateway.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/ws': { target: 'ws://127.0.0.1:7022', ws: true },
      '/api': 'http://127.0.0.1:7022',
      '/healthz': 'http://127.0.0.1:7022',
    },
  },
  build: {
    outDir: '../../server/internal/web/dist',
    emptyOutDir: true,
  },
})
