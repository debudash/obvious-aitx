// Playwright config for the console e2e flows. The Go server must already be
// running on MCPTT_SERVER_ORIGIN (scripts/e2e.sh starts it, seeds it, and
// tears it down); the dev server proxies /api and /ws to it.
import { defineConfig } from '@playwright/test'

const PORT = 5173
const baseURL = `http://localhost:${PORT}`

export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  use: {
    baseURL,
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
  },
  webServer: {
    command: 'npm run dev -- --port 5173 --strictPort',
    url: baseURL,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
})
