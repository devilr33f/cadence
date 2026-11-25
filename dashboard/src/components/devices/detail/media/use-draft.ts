import { useEffect, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'

export const DRAFT_DELAY_MS = 250

// a value a slider moves through many times a second: shown at once, sent once it rests for the delay, so only the
// final one reaches the device. it shows until the send settled (the caller reads the device again inside commit), and
// a failure is said for the newest value only
export function useDraft(commit: (value: number) => Promise<void>, delayMs = DRAFT_DELAY_MS) {
  const [draft, setDraft] = useState<number>()
  const [error, setError] = useState<string>()
  const timerRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const latestRef = useRef<number>(undefined)

  useEffect(() => () => clearTimeout(timerRef.current), [])

  async function send(value: number) {
    try {
      await commit(value)
    }
    catch (failure) {
      if (latestRef.current === value)
        setError(describeError(failure))
    }
    finally {
      if (latestRef.current === value) {
        latestRef.current = undefined
        setDraft(undefined)
      }
    }
  }

  function change(value: number) {
    latestRef.current = value
    setDraft(value)
    setError(undefined)
    clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => void send(value), delayMs)
  }

  return { draft, error, change }
}
