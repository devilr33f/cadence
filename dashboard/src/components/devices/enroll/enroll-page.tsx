import type { EnrollChoice } from './enroll-form'
import type { CreateOutBody } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ChevronLeftIcon } from 'lucide-react'
import { useState } from 'react'
import { useApi } from '@/api/use-api'
import { PageHeader } from '@/components/data/page-header'
import { ErrorState, LoadingLines } from '@/components/data/states'
import { boardsQueryOptions } from '@/lib/devices'
import { useBoardNames } from '../use-boards'
import { EnrollForm } from './enroll-form'
import { EnrollProgress } from './enroll-progress'
import { EnrollStep } from './enroll-step'

function Boards({ initial, onCreated }: { initial?: EnrollChoice, onCreated: (created: CreateOutBody, choice: EnrollChoice) => void }) {
  const api = useApi()
  const boards = useQuery(boardsQueryOptions(api))
  if (boards.isPending)
    return <LoadingLines lines={2} label="Loading the boards" className="max-w-md" />
  if (boards.isError)
    return <ErrorState title="The boards could not be listed" error={boards.error} onRetry={() => void boards.refetch()} className="items-start px-0 py-2 text-left" />
  return <EnrollForm boards={boards.data} initial={initial} onCreated={onCreated} />
}

// the "+" wizard: a board and a name make a device and its one-time code; the speaker runs one command with it; the
// page waits for it to come online. the code lives in this page's state only
export function EnrollPage() {
  const [created, setCreated] = useState<CreateOutBody>()
  const [choice, setChoice] = useState<EnrollChoice>()
  // the pending device an expired code left, once removed: the form opens again on its name
  const [removed, setRemoved] = useState<string>()
  const boards = useBoardNames()

  return (
    <>
      <Link to="/devices" className="mb-3 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
        <ChevronLeftIcon aria-hidden="true" className="size-4" />
        Devices
      </Link>
      <PageHeader
        title="Enroll a speaker"
        description="The speaker joins with a one-time code, counted down here until it expires."
      />
      <ol className="flex max-w-3xl flex-col gap-8">
        <EnrollStep n={1} title="Choose the board and a name">
          {created
            ? <p className="text-sm">{`${created.name}, on the ${boards.get(created.model) ?? created.model} board.`}</p>
            : (
                <>
                  {removed && <p role="status" aria-label="Removed" className="text-sm text-muted-foreground">{`${removed} was removed: its name is free again.`}</p>}
                  <Boards
                    initial={choice}
                    onCreated={(next, picked) => {
                      setChoice(picked)
                      setRemoved(undefined)
                      setCreated(next)
                    }}
                  />
                </>
              )}
        </EnrollStep>
        {created
          ? (
              <EnrollProgress
                key={created.id}
                created={created}
                onRestart={() => setCreated(undefined)}
                onRemoved={() => {
                  setRemoved(created.name)
                  setCreated(undefined)
                }}
              />
            )
          : (
              <>
                <EnrollStep n={2} title="Run the command" waiting>
                  <p className="text-sm text-muted-foreground">The command shows here with the code.</p>
                </EnrollStep>
                <EnrollStep n={3} title="Wait for the speaker" waiting>
                  <p className="text-sm text-muted-foreground">The page follows the speaker until it is online.</p>
                </EnrollStep>
              </>
            )}
      </ol>
    </>
  )
}
