import { defineConfig } from '@hey-api/openapi-ts'

export default defineConfig({
  input: '../server/openapi/openapi.json',
  output: 'src/api/gen',
  plugins: [
    '@hey-api/client-fetch',
    // no shared client: every call names the one src/api/client.ts made, so none can skip its csrf header, error
    // mapping or 401 handling, and an ssr request never borrows another's cookie
    { name: '@hey-api/sdk', client: false },
    '@tanstack/react-query',
  ],
})
