import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Client portal runs on :5174 so it can sit alongside the Studio app (:5173).
// All API calls are proxied to the Go backend — same-origin in dev, no CORS.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5174,
    proxy: {
      '/v1': 'http://localhost:8000',
      '/health': 'http://localhost:8000',
      '/pricing': 'http://localhost:8000',
    },
  },
})
