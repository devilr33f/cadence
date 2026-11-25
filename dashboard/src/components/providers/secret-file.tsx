import { useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { MAX_FILE_BYTES, setFileText } from './secret-file-data'

// a button that reads a text file beside the secret's field: the value never enters the input, whose single-line value
// would drop its line breaks, or react's state; the picker is cleared so the same file can be loaded again
export function SecretFileLoader({ id, target, onLoaded }: {
  id: string
  target: () => HTMLInputElement | undefined
  onLoaded: () => void
}) {
  const pickerRef = useRef<HTMLInputElement>(null)
  const [note, setNote] = useState<string>()
  const load = async (file: File | undefined) => {
    if (!file)
      return
    if (file.size > MAX_FILE_BYTES) {
      setNote('That file is over 512 KiB.')
      return
    }
    const input = target()
    if (!input)
      return
    setFileText(input, await file.text())
    setNote('Loaded from a file.')
    onLoaded()
  }
  return (
    <div className="flex items-center gap-3">
      <input
        ref={pickerRef}
        id={`${id}-file`}
        type="file"
        accept=".txt,text/plain"
        className="sr-only"
        tabIndex={-1}
        aria-hidden
        onChange={(event) => {
          const file = event.currentTarget.files?.[0]
          event.currentTarget.value = ''
          void load(file)
        }}
      />
      <Button type="button" variant="outline" size="sm" onClick={() => pickerRef.current?.click()}>
        Load from file…
      </Button>
      {note !== undefined && <span role="status" className="text-sm text-muted-foreground">{note}</span>}
    </div>
  )
}
