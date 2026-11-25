import type { FormEvent } from 'react'
import type { Item, SearchEvent } from '@/api/gen'
import { LoaderCircleIcon, SearchIcon } from 'lucide-react'
import { useId, useState } from 'react'
import { describeError } from '@/api/describe-error'
import { searchCatalog } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { moduleName } from '@/lib/catalog'

interface Answer {
  key: string
  provider: string
  source?: string
  items: Item[]
  failure?: string
}

// the search's events folded by provider and source: their items in order, or the failure one reported
function answersOf(events: readonly SearchEvent[]): Answer[] {
  const out = new Map<string, Answer>()
  for (const e of events) {
    if (!e.provider)
      continue
    const key = `${e.provider}:${e.source ?? ''}`
    const a = out.get(key) ?? {
      key,
      provider: e.provider,
      source: e.source,
      items: [],
    }
    a.items.push(...(e.items ?? []))
    if (e.failure)
      a.failure = e.failure.code
    out.set(key, a)
  }
  return [
    ...out.values(),
  ]
}

function itemText(item: Item): string {
  const year = item.show?.year
  return year
    ? `${item.title} (${year})`
    : item.title
}

// a search across the catalog's providers, to see that each answers: its items by provider and source, a failure by
// its code. what it finds stays on this page alone
export function CatalogSearch() {
  const api = useApi()
  const id = useId()
  const [query, setQuery] = useState('')
  const [pending, setPending] = useState(false)
  const [answers, setAnswers] = useState<Answer[]>()
  const [error, setError] = useState<string>()

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending || query.trim() === '')
      return
    setPending(true)
    setError(undefined)
    try {
      const { data } = await searchCatalog({
        client: api,
        query: {
          q: query.trim(),
          limit: 10,
        },
        throwOnError: true,
      })
      setAnswers(answersOf(data.events ?? []))
    }
    catch (failure) {
      setAnswers(undefined)
      setError(describeError(failure))
    }
    finally {
      setPending(false)
    }
  }

  return (
    <section aria-labelledby={`${id}-title`} className="flex flex-col gap-3">
      <h2 id={`${id}-title`} className="text-lg font-semibold">Test search</h2>
      <form method="post" role="search" noValidate onSubmit={e => void submit(e)} className="flex flex-wrap items-end gap-2">
        <div className="flex min-w-60 flex-1 flex-col gap-1.5">
          <Label htmlFor={`${id}-query`}>Search the catalog</Label>
          <Input id={`${id}-query`} value={query} onChange={e => setQuery(e.target.value)} />
        </div>
        <Button type="submit" variant="outline" aria-disabled={pending || undefined}>
          {pending
            ? <LoaderCircleIcon aria-hidden="true" className="motion-safe:animate-spin" />
            : <SearchIcon aria-hidden="true" />}
          Search
        </Button>
      </form>
      {error && <p role="alert" className="text-sm text-destructive">{`The search failed. ${error}`}</p>}
      {answers && (
        <div role="status" aria-label="Search results" className="flex flex-col gap-3">
          {answers.length === 0 && <p className="text-sm text-muted-foreground">No provider answered.</p>}
          {answers.map(a => (
            <div
              key={a.key}
              role="group"
              aria-label={a.source
                ? `${moduleName(a.provider)} · ${a.source}`
                : moduleName(a.provider)}
              className="flex flex-col gap-1 rounded-md border p-3"
            >
              <p className="text-sm font-medium">
                {a.source
                  ? `${moduleName(a.provider)} · ${a.source}`
                  : moduleName(a.provider)}
              </p>
              {a.failure
                ? <p className="text-sm text-destructive">{`Failed (${a.failure})`}</p>
                : a.items.length === 0
                  ? <p className="text-sm text-muted-foreground">Nothing found.</p>
                  : (
                      <ul className="text-sm">
                        {a.items.map(item => (
                          <li key={item.ref ?? `${item.kind}:${item.title}:${item.show?.year ?? ''}`}>
                            {itemText(item)}
                            <span className="ml-2 text-xs text-muted-foreground">{item.kind}</span>
                          </li>
                        ))}
                      </ul>
                    )}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
