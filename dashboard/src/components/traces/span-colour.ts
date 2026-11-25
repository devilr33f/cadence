import type { SpanView } from '@/api/gen'
import { SERVICE_DEVICE, SERVICE_SERVER } from '@/lib/traces/format'

// a bar's fill by where it ran, a failed one red whatever ran it; never the yellow (DESIGN.md, Colors)
export function barColour(span: SpanView): string {
  if (span.status === 'error')
    return 'bg-destructive'
  switch (span.service) {
    case SERVICE_SERVER:
      return 'bg-chart-1'
    case SERVICE_DEVICE:
      return 'bg-chart-2'
    default:
      return 'bg-chart-4'
  }
}
