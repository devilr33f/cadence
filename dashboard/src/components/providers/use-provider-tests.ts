import type { Instance, TestResult } from '@/api/gen'
import { useEffect, useRef, useState } from 'react'
import { ApiError } from '@/api/error'

// a 429 without a Retry-After waits the server's documented window
const SERVER_WINDOW_S = 5

export type TestOutcome = { result: TestResult } | { error: unknown }

export interface ProviderTest {
  running: boolean
  // the last outcome this page saw
  outcome?: TestOutcome
  // the server refused another test until then (429), for this many seconds: the button waits
  until?: number
  wait?: number
}

// each instance's last test on this page, by instance id (a new instance of a deleted one's name starts blank). one test
// at a time per instance, and none while the server's Retry-After runs: the server allows one per instance per 5 s, so a
// double click or an impatient one never reaches it twice
export function useProviderTests(test: (name: string) => Promise<TestResult>) {
  const [tests, setTests] = useState<Readonly<Record<string, ProviderTest>>>({})
  const busyRef = useRef(new Set<string>())
  const timersRef = useRef(new Set<ReturnType<typeof setTimeout>>())

  useEffect(() => {
    const pending = timersRef.current
    return () => pending.forEach(clearTimeout)
  }, [])

  const update = (id: string, change: (current: ProviderTest) => ProviderTest) =>
    setTests(all => ({
      ...all,
      [id]: change(all[id] ?? { running: false }),
    }))

  async function run(instance: Instance) {
    const { id } = instance
    const until = tests[id]?.until
    if (busyRef.current.has(id) || (until !== undefined && Date.now() < until))
      return
    busyRef.current.add(id)
    update(id, current => ({ ...current, running: true }))
    try {
      const result = await test(instance.name)
      update(id, () => ({
        running: false,
        outcome: { result },
      }))
    }
    catch (error) {
      if (error instanceof ApiError && error.status === 429) {
        const wait = error.retryAfter ?? SERVER_WINDOW_S
        update(id, current => ({
          ...current,
          running: false,
          until: Date.now() + wait * 1000,
          wait,
        }))
        // the wait ends here, so its countdown stops ticking
        const timer = setTimeout(() => {
          timersRef.current.delete(timer)
          update(id, ({ until: _, wait: __, ...rest }) => rest)
        }, wait * 1000)
        timersRef.current.add(timer)
      }
      else {
        update(id, () => ({
          running: false,
          outcome: { error },
        }))
      }
    }
    finally {
      busyRef.current.delete(id)
    }
  }

  return { tests, run }
}
