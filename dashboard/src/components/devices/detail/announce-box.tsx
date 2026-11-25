import type { FormEvent } from 'react'
import { LoaderCircleIcon, MegaphoneIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { announceToDevice } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

// the server's bound on an announcement, once trimmed
const MAX_TEXT = 500

type Said = { status: 'idle' | 'pending' } | { status: 'sent' } | { status: 'failed', message: string }

// text the speaker says over whatever plays (the server ducks the music); 202 once its first frame went out
export function AnnounceBox({ deviceId, deviceName }: { deviceId: string, deviceName: string }) {
  const api = useApi()
  const titleId = useId()
  const fieldId = useId()
  const [text, setText] = useState('')
  const [said, setSaid] = useState<Said>({ status: 'idle' })
  const trimmed = text.trim()
  const tooLong = [...trimmed].length > MAX_TEXT

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (trimmed === '' || tooLong || said.status === 'pending')
      return
    setSaid({ status: 'pending' })
    try {
      await announceToDevice({
        client: api,
        path: { id: deviceId },
        body: { text: trimmed },
        throwOnError: true,
      })
      setText('')
      setSaid({ status: 'sent' })
    }
    catch (error) {
      setSaid({
        status: 'failed',
        message: describeError(error),
      })
    }
  }

  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader>
        <h2 id={titleId} className="font-heading text-base font-semibold">Say</h2>
      </CardHeader>
      <CardContent>
        <form method="post" onSubmit={e => void submit(e)} className="flex flex-col gap-3">
          <Label htmlFor={fieldId}>Text to say</Label>
          <Textarea
            id={fieldId}
            value={text}
            rows={2}
            aria-invalid={tooLong || undefined}
            aria-describedby={`${fieldId}-count`}
            onChange={(e) => {
              setText(e.target.value)
              if (said.status !== 'pending')
                setSaid({ status: 'idle' })
            }}
          />
          <p id={`${fieldId}-count`} className={tooLong ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>
            {`${[...trimmed].length} of ${MAX_TEXT} characters`}
          </p>
          <div className="flex flex-wrap items-center gap-3">
            {/* focusable throughout: a disabled button would drop the focus to the page as it is sent or emptied */}
            <Button type="submit" variant="outline" aria-disabled={trimmed === '' || tooLong || said.status === 'pending' || undefined}>
              {said.status === 'pending'
                ? <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />
                : <MegaphoneIcon aria-hidden="true" />}
              Say it
            </Button>
            <p role="status" className="text-sm text-muted-foreground">
              {said.status === 'sent'
                ? `Sent to ${deviceName}.`
                : ''}
            </p>
          </div>
          {said.status === 'failed' && <p role="alert" className="text-sm text-destructive">{said.message}</p>}
        </form>
      </CardContent>
    </Card>
  )
}
