import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Dev server proxies API calls to the Go server so `npm run dev` works
// against a locally running `cogitorium serve`.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8688',
      '/health': 'http://127.0.0.1:8688',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
