// the image's HEALTHCHECK: the server's own health route, from inside its container (node has no curl beside it)
import process from 'node:process'
import { HEALTH_PATH } from './handler.ts'

fetch(`http://127.0.0.1:3000${HEALTH_PATH}`, {
  signal: AbortSignal.timeout(2_000),
}).then(
  answer => process.exit(answer.ok ? 0 : 1),
  () => process.exit(1),
)
