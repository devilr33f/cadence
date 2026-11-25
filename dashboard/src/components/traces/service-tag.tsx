import { Tag } from '@/components/data/tag'
import { serviceLabel } from '@/lib/traces/format'

// where a span ran: the server or a speaker
export function ServiceTag({ service }: { service: string }) {
  return <Tag>{serviceLabel(service)}</Tag>
}
