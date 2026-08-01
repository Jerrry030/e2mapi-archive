import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The console is served same-origin, embedded into the e2m-core Go binary at /.
// In dev, proxy API + health to the local core so client code always calls
// relative URLs (identical dev/prod) and CORS stays off.
export default defineConfig({
  base: '/',
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
      '/healthz': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
