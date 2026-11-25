import type { FormEvent, KeyboardEvent } from 'react'
import type { TurnView } from './assistant-turn'
import { BotIcon, LoaderCircleIcon, SendHorizontalIcon, UnplugIcon } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { startAssistantTurn } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { EmptyState } from '@/components/data/states'
import { Timeline } from '@/components/data/timeline'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { useLiveStatus, useLiveTimeline } from '@/lib/live/use-live'
import { useTimeZone } from '@/lib/time-zone'
import { turnView } from './assistant-turn'

// the server's bound on a typed request, once trimmed
const MAX_TEXT = 1000

interface Asked {
  key: number
  text: string
  speak: boolean
  turnId?: string
  error?: string
}

// what the speaker's announcement answered, in words
const SPEECH: Readonly<Record<string, string>> = {
  ok: 'Spoken on the device.',
  device_offline: 'Not spoken: the device is offline.',
  no_audio: 'Not spoken: the device reported no audio output.',
  busy: 'Not spoken: a voice session was running.',
  tts_unconfigured: 'Not spoken: no TTS provider is set.',
  superseded: 'Not spoken in full: a newer announcement replaced it.',
  tts_failed: 'Not spoken: TTS failed.',
  tts_timeout: 'Not spoken: TTS gave no audio in time.',
}

function endText(end: NonNullable<TurnView['end']>): string {
  const parts = [`${end.steps} step${end.steps === 1
    ? ''
    : 's'}`, `${end.toolCalls} tool call${end.toolCalls === 1
    ? ''
    : 's'}`, `${(end.ms / 1000).toFixed(1)} s`]
  return end.code === 'ok'
    ? parts.join(' · ')
    : `Failed: ${end.code} · ${parts.join(' · ')}`
}

function Turn({ asked, events, deviceName }: { asked: Asked, events: ReturnType<typeof useLiveTimeline>, deviceName: string }) {
  const timeZone = useTimeZone()
  const view = useMemo(() => asked.turnId
    ? turnView(events, asked.turnId)
    : undefined, [events, asked.turnId])
  const running = asked.turnId !== undefined && !view?.end
  return (
    <li className="flex flex-col gap-3">
      <div className="ml-auto max-w-[85%] rounded-lg bg-muted px-3 py-2 text-sm whitespace-pre-wrap">{asked.text}</div>
      {asked.error && <p role="alert" className="text-sm text-destructive">{asked.error}</p>}
      {view && view.steps.length > 0 && (
        <div className="rounded-lg border bg-card px-4 pt-4">
          <Timeline label={`Steps of the turn «${asked.text.slice(0, 40)}»`} entries={view.steps} timeZone={timeZone} />
        </div>
      )}
      {(running || (asked.turnId === undefined && !asked.error)) && (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <LoaderCircleIcon aria-hidden="true" className="size-4 motion-safe:animate-spin" />
          Thinking…
        </p>
      )}
      {view?.end && (
        <div className="flex max-w-[85%] flex-col gap-1">
          {view.reply
            ? (
                <div className="flex gap-2 rounded-lg border bg-card px-3 py-2 text-sm whitespace-pre-wrap">
                  <BotIcon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <span>{view.reply}</span>
                </div>
              )
            : (
                <p className="text-sm text-muted-foreground italic">
                  {view.end.code === 'ok'
                    ? `${deviceName} said nothing.`
                    : 'No reply.'}
                </p>
              )}
          <p className={view.end.code === 'ok'
            ? 'text-xs text-muted-foreground'
            : 'text-xs text-destructive'}
          >
            {endText(view.end)}
            {asked.speak && view.speech && ` · ${SPEECH[view.speech] ?? `Not spoken: ${view.speech}.`}`}
          </p>
        </div>
      )}
    </li>
  )
}

// requests typed for the device's assistant, each run as the device's own turn woken by a press: its tools act for
// real, it joins the device's conversation, and the live feed shows its steps as they happen
export function AssistantChat({ deviceId, deviceName }: { deviceId: string, deviceName: string }) {
  const api = useApi()
  const events = useLiveTimeline()
  const status = useLiveStatus()
  const fieldId = useId()
  const speakId = useId()
  const [text, setText] = useState('')
  const [speak, setSpeak] = useState(false)
  const [asked, setAsked] = useState<Asked[]>([])
  const [sending, setSending] = useState(false)
  const trimmed = text.trim()
  const tooLong = [...trimmed].length > MAX_TEXT
  const blocked = trimmed === '' || tooLong || sending

  async function send(event?: FormEvent<HTMLFormElement>) {
    event?.preventDefault()
    if (blocked)
      return
    const key = Date.now()
    const entry: Asked = { key, text: trimmed, speak }
    const update = (patch: Partial<Asked>) => setAsked(all => all.map(a => a.key === key
      ? {
          ...a,
          ...patch,
        }
      : a))
    setAsked(all => [...all, entry])
    setText('')
    setSending(true)
    try {
      const { data } = await startAssistantTurn({
        client: api,
        path: { id: deviceId },
        body: { text: trimmed, speak },
        throwOnError: true,
      })
      update({ turnId: data.turnId })
    }
    catch (error) {
      update({
        error: describeError(error),
      })
    }
    finally {
      setSending(false)
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault()
      void send()
    }
  }

  return (
    <div className="flex flex-col gap-6">
      {status !== 'live' && status !== 'connecting' && (
        <EmptyState icon={UnplugIcon} title="The live feed is offline" description="Turns still run, but their steps show here only once it reconnects." />
      )}
      {asked.length === 0
        ? (
            <p className="text-sm text-muted-foreground">
              {`Type a request as if you said it to ${deviceName}. It runs as a press would: its tools act for real, the reply joins the device's conversation, and every step shows here.`}
            </p>
          )
        : (
            <ol aria-label={`Requests to ${deviceName}`} className="flex flex-col gap-6">
              {asked.map(a => <Turn key={a.key} asked={a} events={events} deviceName={deviceName} />)}
            </ol>
          )}
      <form method="post" onSubmit={e => void send(e)} className="flex flex-col gap-3 rounded-lg border bg-card p-4">
        <Label htmlFor={fieldId}>Request</Label>
        <Textarea
          id={fieldId}
          value={text}
          rows={2}
          placeholder="Включи свет на кухне"
          aria-invalid={tooLong || undefined}
          aria-describedby={`${fieldId}-count`}
          onChange={e => setText(e.target.value)}
          onKeyDown={onKeyDown}
        />
        <p id={`${fieldId}-count`} className={tooLong ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>
          {`${[...trimmed].length} of ${MAX_TEXT} characters · Enter sends, Shift+Enter breaks the line`}
        </p>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <Switch id={speakId} checked={speak} onCheckedChange={setSpeak} />
            <Label htmlFor={speakId}>Speak the reply on the device</Label>
          </div>
          {/* focusable throughout: a disabled button would drop the focus to the page as it is sent or emptied */}
          <Button type="submit" aria-disabled={blocked || undefined}>
            {sending
              ? <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />
              : <SendHorizontalIcon aria-hidden="true" />}
            Send
          </Button>
        </div>
      </form>
    </div>
  )
}
