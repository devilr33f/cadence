import type { ReactNode } from 'react'
import { LoaderCircleIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'

// a list's next page, below its table. while it loads the button stays focusable (aria-disabled, clicks ignored), since
// a disabled one would drop the focus to the page
export function MoreButton({ busy, onMore, children }: { busy: boolean, onMore: () => void, children: ReactNode }) {
  return (
    <Button type="button" variant="outline" className="self-start" aria-disabled={busy || undefined} onClick={() => !busy && onMore()}>
      {busy && <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />}
      {children}
    </Button>
  )
}
