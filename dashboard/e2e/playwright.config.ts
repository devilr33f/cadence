import process from 'node:process'
import { defineConfig, devices } from '@playwright/test'

const origin = process.env.CADENCE_E2E_ORIGIN ?? 'http://127.0.0.1:8080'
const { hostname } = new URL(origin)
// the browser runs in the edge's network namespace, where the edge listens on the origin's port: an origin named for
// the host (ci's docker service) resolves there to the edge itself, so the Origin the server checks stays that name
const resolve = ['127.0.0.1', 'localhost', '[::1]'].includes(hostname)
  ? []
  : [`--host-resolver-rules=MAP ${hostname} 127.0.0.1`]

// the dashboard against the dev stack, driven by deploy/dev/playwright.sh in the pinned playwright image (README, the
// e2e). no trace: one records the login's body and every typed value (the password, a secret); a failure leaves a
// screenshot with every field and the token box masked (support.ts). the specs call the api from the page alone
// (fetch in the browser), which the resolver rule covers
export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  outputDir: process.env.CADENCE_E2E_OUTPUT ?? '../../build/dashboard-e2e',
  forbidOnly: true,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [
    ['list'],
  ],
  timeout: 90_000,
  expect: { timeout: 15_000 },
  use: {
    ...devices['Desktop Chrome'],
    baseURL: origin,
    launchOptions: { args: resolve },
    trace: 'off',
    screenshot: 'off',
    video: 'off',
  },
})
