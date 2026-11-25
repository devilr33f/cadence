import type { FormEvent } from 'react'
import { useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ApiError } from '@/api/error'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { datasetCodeText } from '@/lib/wakewords'

// a refusal as the owner reads it: a dataset's own code by its words, else the server's answer
function refusalText(error: unknown): string {
  if (error instanceof ApiError && error.error === 'invalid_dataset')
    return datasetCodeText(error.detail) ?? describeError(error)
  if (error instanceof ApiError && error.error === 'negatives_unconfigured')
    return 'The server has no negatives file set (CADENCE_WAKEWORD_NEGATIVES), so it cannot train.'
  return describeError(error)
}

// the dataset zip and the word: the page's one primary action
export function TrainForm({ onTrain }: { onTrain: (file: File, word: string, keep: boolean) => Promise<void> }) {
  const wordId = useId()
  const fileId = useId()
  const keepId = useId()
  const errorId = useId()
  const [word, setWord] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [keep, setKeep] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<{ field?: 'word' | 'file', text: string }>()
  const wordRef = useRef<HTMLInputElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)
  const formRef = useRef<HTMLFormElement>(null)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending)
      return
    if (!word.trim()) {
      setError({ field: 'word', text: 'Name the word.' })
      wordRef.current?.focus()
      return
    }
    if (!file) {
      setError({ field: 'file', text: 'Choose the dataset zip.' })
      fileRef.current?.focus()
      return
    }
    setPending(true)
    setError(undefined)
    try {
      await onTrain(file, word.trim(), keep)
      setWord('')
      setFile(null)
      formRef.current?.reset()
    }
    catch (failure) {
      setError({
        text: refusalText(failure),
      })
    }
    finally {
      setPending(false)
    }
  }

  return (
    <form method="post" ref={formRef} className="flex max-w-xl flex-col gap-5 rounded-lg border bg-card p-6" onSubmit={e => void submit(e)}>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={wordId}>
          Word
          <span className="text-destructive" aria-label="required">*</span>
        </Label>
        <Input
          ref={wordRef}
          id={wordId}
          value={word}
          maxLength={64}
          autoComplete="off"
          aria-invalid={error?.field === 'word' || undefined}
          aria-describedby={error?.field === 'word'
            ? errorId
            : undefined}
          onChange={e => setWord(e.target.value)}
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={fileId}>
          Dataset zip
          <span className="text-destructive" aria-label="required">*</span>
        </Label>
        <input
          ref={fileRef}
          id={fileId}
          type="file"
          accept=".zip,application/zip"
          className="text-sm"
          aria-invalid={error?.field === 'file' || undefined}
          aria-describedby={error?.field === 'file'
            ? errorId
            : undefined}
          onChange={e => setFile(e.target.files?.[0] ?? null)}
        />
        <p className="max-w-[65ch] text-sm text-muted-foreground">
          WAV files, any rate: a folder
          {' '}
          <code className="font-mono text-xs">takes</code>
          {' '}
          with 6 to 15 takes of the word, one each, said where the speaker stands; an
          {' '}
          <code className="font-mono text-xs">ambient.wav</code>
          {' '}
          of the room for 10 seconds or more; and, if you like, a
          {' '}
          <code className="font-mono text-xs">speech.wav</code>
          {' '}
          of you talking normally.
        </p>
      </div>
      <div className="flex items-center gap-3">
        <Switch id={keepId} checked={keep} onCheckedChange={setKeep} />
        <Label htmlFor={keepId}>Keep the recordings for a retrain</Label>
      </div>
      <p className="-mt-3 text-sm text-muted-foreground">
        Off, the server drops them once the training ends. Kept, they stay in its database until you delete them.
      </p>
      <div className="flex flex-col items-start gap-2">
        <Button type="submit" size="lg" aria-disabled={pending || undefined}>
          {pending ? 'Uploading…' : 'Train the word'}
        </Button>
        {error && <p id={errorId} role="alert" className="text-sm text-destructive">{error.text}</p>}
      </div>
    </form>
  )
}
