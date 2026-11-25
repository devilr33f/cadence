import viteReact from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  resolve: {
    tsconfigPaths: true,
  },
  plugins: [
    viteReact(),
  ],
  test: {
    include: ['src/**/*.test.{ts,tsx}', 'serve/**/*.test.ts'],
    setupFiles: ['src/test/setup.ts'],
    // headroom for make ci's load, beside the go and c++ builds (src/test/setup.ts)
    testTimeout: 15_000,
  },
})
