import type { FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { LoaderCircleIcon, PlayIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { sendDeviceCommand } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { deviceMediaQueryOptions } from '@/lib/devices'

// media.url's bound on a url (audiod's frame), and the schemes it plays
const MAX_URL = 2047
const WEB_URL = /^https?:\/\/./i

type Played = { status: 'idle' | 'pending' | 'sent' } | { status: 'failed', message: string }

// what is wrong with the url as typed, or nothing; an empty field says nothing until it is sent
function urlProblem(url: string): string | undefined {
  if (url === '')
    return undefined
  if (!WEB_URL.test(url))
    return 'Starts with http:// or https://.'
  if (new TextEncoder().encode(url).length > MAX_URL)
    return `At most ${MAX_URL} bytes.`
  return undefined
}

// any http or https url (a radio stream, a file) as the device's media session, through its media.url capability;
// audiod's ffmpeg reads it, the stream's icy title becomes the session's
export function PlayUrlBox({ deviceId, deviceName }: { deviceId: string, deviceName: string }) {
  const api = useApi()
  const queryClient = useQueryClient()
  const titleId = useId()
  const fieldId = useId()
  const [url, setUrl] = useState('')
  const [played, setPlayed] = useState<Played>({ status: 'idle' })
  const trimmed = url.trim()
  const problem = urlProblem(trimmed)
  const blocked = trimmed === '' || problem !== undefined || played.status === 'pending'

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (blocked)
      return
    setPlayed({ status: 'pending' })
    try {
      await sendDeviceCommand({
        client: api,
        path: { id: deviceId, cap: 'media.url', name: 'play' },
        body: { url: trimmed, mode: 'session' },
        throwOnError: true,
      })
      setPlayed({ status: 'sent' })
    }
    catch (error) {
      setPlayed({
        status: 'failed',
        message: describeError(error),
      })
    }
    finally {
      void queryClient.invalidateQueries({ queryKey: deviceMediaQueryOptions(api, deviceId).queryKey })
    }
  }

  return (
    <Card size="sm" role="region" aria-labelledby={titleId}>
      <CardHeader>
        <h2 id={titleId} className="font-heading text-base font-semibold">Play a URL</h2>
      </CardHeader>
      <CardContent>
        <form method="post" onSubmit={e => void submit(e)} className="flex flex-col gap-3">
          <Label htmlFor={fieldId}>Stream or file URL</Label>
          <Input
            id={fieldId}
            type="url"
            inputMode="url"
            autoComplete="off"
            spellCheck={false}
            placeholder="https://"
            value={url}
            aria-invalid={problem !== undefined || undefined}
            aria-describedby={problem
              ? `${fieldId}-problem`
              : undefined}
            onChange={(e) => {
              setUrl(e.target.value)
              if (played.status !== 'pending')
                setPlayed({ status: 'idle' })
            }}
          />
          {problem && <p id={`${fieldId}-problem`} className="text-xs text-destructive">{problem}</p>}
          <div className="flex flex-wrap items-center gap-3">
            {/* focusable throughout: a disabled button would drop the focus to the page as it is sent */}
            <Button type="submit" variant="outline" aria-disabled={blocked || undefined}>
              {played.status === 'pending'
                ? <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />
                : <PlayIcon aria-hidden="true" />}
              Play
            </Button>
            <p role="status" className="text-sm text-muted-foreground">
              {played.status === 'sent'
                ? `Playing on ${deviceName}.`
                : ''}
            </p>
          </div>
          {played.status === 'failed' && <p role="alert" className="text-sm text-destructive">{played.message}</p>}
        </form>
      </CardContent>
    </Card>
  )
}
