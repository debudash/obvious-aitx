import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// The server sets no CORS headers, so the console is always served same-origin
// with a proxy to the MCPTT server: dev and preview forward /api (REST) and
// /ws (WebSocket) to MCPTT_SERVER_ORIGIN (default http://localhost:8080).
export const serverOrigin = process.env.MCPTT_SERVER_ORIGIN ?? 'http://localhost:8080'

const proxy = {
  '/api': { target: serverOrigin, changeOrigin: true },
  '/ws': { target: serverOrigin, ws: true, changeOrigin: true },
}

export default defineConfig({
  plugins: [react()],
  server: { port: 5173, proxy },
  preview: { port: 4173, proxy },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
  },
})
