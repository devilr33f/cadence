import type { ModeTarget } from './mode-dialog'
import { useQuery } from '@tanstack/react-query'
import { PlusIcon, SlidersHorizontalIcon } from 'lucide-react'
import { useRef, useState } from 'react'
import { useApi } from '@/api/use-api'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { DataTable } from '@/components/data/data-table'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState } from '@/components/data/states'
import { SettingLink } from '@/components/integrations/setting-link'
import { Button } from '@/components/ui/button'
import { modesQueryOptions, useModeActions } from '@/lib/modes'
import { ModeDialog } from './mode-dialog'
import { MODE_COLUMNS } from './mode-table'

const BY_NAME = { column: 'name', direction: 'asc' } as const

export function ModesPage() {
  const api = useApi()
  const list = useQuery(modesQueryOptions(api))
  const actions = useModeActions()
  const [target, setTarget] = useState<ModeTarget>()
  // a create and every delete give the focus back to the page's action (the empty state's button, or the deleted row's,
  // is gone by then); an edit to the row's Edit button that opened it
  const addRef = useRef<HTMLButtonElement>(null)
  const openerRef = useRef<HTMLElement | null>(null)

  // a create checks its name against the list's: until the list is read, one could replace a mode (the table says why)
  const listRead = list.data !== undefined

  const open = (next: ModeTarget, opener: HTMLElement | null) => {
    openerRef.current = next.kind === 'edit'
      ? opener
      : addRef.current
    setTarget(next)
  }
  const add = (size: 'default' | 'lg') => (
    <Button
      ref={size === 'lg'
        ? addRef
        : undefined}
      size={size}
      aria-disabled={!listRead || undefined}
      onClick={(e) => {
        if (listRead)
          open({ kind: 'create' }, e.currentTarget)
      }}
    >
      <PlusIcon aria-hidden="true" />
      Add a mode
    </Button>
  )

  return (
    <>
      <PageHeader
        title="Modes"
        description="Presets for a turn: its model, reasoning and budgets, the tools it offers and the words that switch to it by voice."
        actions={add('lg')}
      />
      {/* the settings say which mode a turn starts in and which a voice may switch to; a mode they name stays */}
      <div className="-mt-3 mb-5 flex flex-wrap items-center gap-x-3 text-sm text-muted-foreground">
        <span>Chosen in the settings:</span>
        <SettingLink settingKey="mode" />
        <SettingLink settingKey="allowedModes" />
      </div>
      <DataTable
        caption="Modes"
        columns={MODE_COLUMNS}
        rows={list.data}
        rowKey={m => m.name}
        initialSort={BY_NAME}
        status={list.isError
          ? 'error'
          : list.isPending ? 'loading' : 'ready'}
        error={<ErrorState title="The modes could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
        empty={(
          <EmptyState
            icon={SlidersHorizontalIcon}
            title="No modes"
            description="A mode sets a turn's model, reasoning and budgets; the settings name the one a turn starts in."
            action={add('default')}
          />
        )}
        rowActions={mode => (
          <div className="flex justify-end gap-1">
            <Button type="button" variant="ghost" size="sm" aria-label={`Edit ${mode.name}`} onClick={e => open({ kind: 'edit', mode }, e.currentTarget)}>
              Edit
            </Button>
            <ConfirmDialog
              trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Delete ${mode.name}`}>Delete</Button>}
              title={`Delete ${mode.name}?`}
              description="The server refuses while a setting names it (in_use): the global mode or allowedModes, the defaults fast and smart included, or a speaker's own."
              confirmLabel="Delete the mode"
              tone="destructive"
              inlineError
              finalFocus={addRef}
              onConfirm={() => actions.remove(mode.name)}
            />
          </div>
        )}
      />
      <ModeDialog
        target={target}
        onClose={() => setTarget(undefined)}
        finalFocus={openerRef}
        taken={list.data?.map(m => m.name) ?? []}
        onSave={actions.put}
      />
    </>
  )
}
