import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [
    react(),
    {
      name: 'catsco-preview-identity',
      configureServer(server) {
        server.middlewares.use('/__catsco_preview', (_request, response) => {
          response.setHeader('Content-Type', 'application/json; charset=utf-8')
          response.end(JSON.stringify({ root: server.config.root }))
        })
      },
    },
  ],
  server: {
    strictPort: true,
  },
  preview: {
    strictPort: true,
  },
})
