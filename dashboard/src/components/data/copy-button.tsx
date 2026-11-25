import { CheckIcon, CopyIcon } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'

const SHOWN_MS = 2000

// the clipboard needs a secure origin; where it is refused the reader selects the text instead
export function CopyButton({ text, label, autoFocus = false }: { text: string, label: string, autoFocus?: boolean }) {
  const [state, setState] = useState<'idle' | 'copied' | 'refused'>('idle')

  useEffect(() => {
    if (state !== 'copied')
      return
    const timer = setTimeout(setState, SHOWN_MS, 'idle')
    return () => clearTimeout(timer)
  }, [state])

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setState('copied')
    }
    catch {
      setState('refused')
    }
  }

  return (
    <div className="flex items-center gap-3">
      <Button type="button" variant="outline" autoFocus={autoFocus} onClick={() => void copy()}>
        {state === 'copied'
          ? <CheckIcon aria-hidden="true" />
          : <CopyIcon aria-hidden="true" />}
        {state === 'copied'
          ? 'Copied'
          : label}
      </Button>
      {state === 'refused' && <p role="alert" className="text-sm text-muted-foreground">This browser refused the clipboard: select the text and copy it.</p>}
    </div>
  )
}
