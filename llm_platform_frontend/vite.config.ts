import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/run': 'http://localhost:8000',
      '/sessions': 'http://localhost:8000',
      '/health': 'http://localhost:8000',
      '/models': 'http://localhost:8000',
      '/ratings': 'http://localhost:8000',
      '/pricing': 'http://localhost:8000',
    },
  },
})
