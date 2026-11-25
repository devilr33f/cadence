import process from 'node:process'
import tailwindcss from '@tailwindcss/vite'
import { tanstackStart } from '@tanstack/react-start/plugin/vite'
import viteReact from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const apiPort = process.env.CADENCE_DEV_PORT ?? '8080'

export default defineConfig({
  resolve: {
    tsconfigPaths: true,
  },
  server: {
    port: 3000,
    strictPort: true,
    proxy: {
      // the browser's Origin must reach the server unchanged: it checks it against CADENCE_PUBLIC_ORIGIN, which a
      // local stack sets to this dev server's origin, and the live feed's websocket upgrade carries it too
      '/api': {
        target: `http://127.0.0.1:${apiPort}`,
        ws: true,
        changeOrigin: false,
      },
    },
  },
  plugins: [
    tailwindcss(),
    tanstackStart(),
    // react's plugin must come after start's
    viteReact(),
  ],
})
