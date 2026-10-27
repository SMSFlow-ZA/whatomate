import { defineConfig } from '@playwright/test'
import { fileURLToPath } from 'node:url'
export default defineConfig({
  testDir: './layout', workers: 1, reporter: 'list',
  use: { baseURL: process.env.SCROLL_TEST_URL || 'http://127.0.0.1:18337', browserName: 'chromium', headless: true, launchOptions: { executablePath: process.env.SCROLL_TEST_CHROMIUM } },
  webServer: process.env.SCROLL_TEST_URL ? undefined : { cwd: fileURLToPath(new URL('..', import.meta.url)), command: `"${process.execPath}" node_modules/vite/bin/vite.js --host 127.0.0.1 --port 18337 --strictPort`, url: 'http://127.0.0.1:18337', reuseExistingServer: false },
})
