import { cn } from 'cn'
import { XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'

// a list's items as chips, each with its remove button; ids in mono, words in the body face
export function TagChips({ label, items, onRemove, mono = true }: {
  label: string
  items: readonly (string | number)[]
  onRemove: (index: number) => void
  mono?: boolean
}) {
  if (items.length === 0)
    return null
  return (
    <ul aria-label={`Items of ${label}`} className="flex flex-wrap gap-1.5">
      {items.map((item, i) => (
        // eslint-disable-next-line react/no-array-index-key -- a list may hold one item twice until it is checked
        <li key={`${i}-${item}`} className="inline-flex h-7 items-center gap-1 rounded-md border bg-muted pr-0.5 pl-2 text-sm">
          <span className={cn(mono ? 'font-mono text-xs' : 'text-sm')}>{String(item)}</span>
          <Button type="button" variant="ghost" size="icon-xs" aria-label={`Remove ${item}`} onClick={() => onRemove(i)}>
            <XIcon aria-hidden="true" />
          </Button>
        </li>
      ))}
    </ul>
  )
}
