import type { FormEvent } from 'react'
import { useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

// a model trained elsewhere (or downloaded from another server): its JSON file, and a label in place of its own word
export function UploadModelForm({ onUpload }: { onUpload: (file: File, word: string) => Promise<void> }) {
  const wordId = useId()
  const fileId = useId()
  const errorId = useId()
  const [word, setWord] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const fileRef = useRef<HTMLInputElement>(null)
  const formRef = useRef<HTMLFormElement>(null)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    if (!file) {
      setError('Choose the model file.')
      fileRef.current?.focus()
      return
    }
    setPending(true)
    setError(undefined)
    try {
      await onUpload(file, word.trim())
      setWord('')
      setFile(null)
      formRef.current?.reset()
    }
    catch (failure) {
      setError(failure instanceof ApiError && failure.error === 'invalid_model'
        ? 'That file is not a Cadence wake word model.'
        : describeError(failure))
    }
    finally {
      setPending(false)
    }
  }

  return (
    <form method="post" ref={formRef} className="flex max-w-xl flex-col gap-5 rounded-lg border bg-card p-6" onSubmit={e => void submit(e)}>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={fileId}>
          Model file
          <span className="text-destructive" aria-label="required">*</span>
        </Label>
        <input
          ref={fileRef}
          id={fileId}
          type="file"
          accept=".json,application/json"
          className="text-sm"
          aria-invalid={error !== undefined || undefined}
          aria-describedby={error ? errorId : undefined}
          onChange={e => setFile(e.target.files?.[0] ?? null)}
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={wordId}>Word</Label>
        <Input id={wordId} value={word} maxLength={64} autoComplete="off" onChange={e => setWord(e.target.value)} />
        <p className="text-sm text-muted-foreground">Left empty, the model&apos;s own word names it.</p>
      </div>
      <div className="flex flex-col items-start gap-2">
        <Button type="submit" variant="secondary" aria-disabled={pending || undefined}>
          {pending ? 'Uploading…' : 'Upload the model'}
        </Button>
        {error && <p id={errorId} role="alert" className="text-sm text-destructive">{error}</p>}
      </div>
    </form>
  )
}
