import type { SkillTarget } from './skill-dialog'
import { useQuery } from '@tanstack/react-query'
import { BookOpenIcon, PlusIcon } from 'lucide-react'
import { useRef, useState } from 'react'
import { useApi } from '@/api/use-api'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { DataTable } from '@/components/data/data-table'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { skillsQueryOptions, useSkillActions } from '@/lib/skills'
import { SkillDialog } from './skill-dialog'
import { SKILL_COLUMNS } from './skill-table'

const BY_NAME = { column: 'name', direction: 'asc' } as const

export function SkillsPage() {
  const api = useApi()
  const list = useQuery(skillsQueryOptions(api))
  const actions = useSkillActions()
  const [target, setTarget] = useState<SkillTarget>()
  // a create and every delete give the focus back to the page's action; an edit to the row's button that opened it
  const addRef = useRef<HTMLButtonElement>(null)
  const openerRef = useRef<HTMLElement | null>(null)
  // a create checks its name against the list's: until the list is read, one could replace a skill
  const listRead = list.data !== undefined

  const open = (next: SkillTarget, opener: HTMLElement | null) => {
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
      Add a skill
    </Button>
  )

  return (
    <>
      <PageHeader
        title="Skills"
        description="Instructions a turn loads only when what was said calls for them: by trigger words, by meaning when an embedding provider is set, or when the model asks for one."
        actions={add('lg')}
      />
      <DataTable
        caption="Skills"
        columns={SKILL_COLUMNS}
        rows={list.data}
        rowKey={s => s.name}
        initialSort={BY_NAME}
        status={list.isError
          ? 'error'
          : list.isPending ? 'loading' : 'ready'}
        error={<ErrorState title="The skills could not be listed" error={list.error} onRetry={() => void list.refetch()} />}
        empty={(
          <EmptyState
            icon={BookOpenIcon}
            title="No skills"
            description="A skill is a short set of instructions for one kind of request, loaded only into the turns that need it."
            action={add('default')}
          />
        )}
        rowActions={skill => (
          <div className="flex justify-end gap-1">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              aria-label={`${skill.source === 'builtin'
                ? 'Customize'
                : 'Edit'} ${skill.name}`}
              onClick={e => open({ kind: 'edit', skill }, e.currentTarget)}
            >
              {skill.source === 'builtin'
                ? 'Customize'
                : 'Edit'}
            </Button>
            {skill.source === 'owner' && (
              <ConfirmDialog
                trigger={(
                  <Button type="button" variant="ghost" size="sm" aria-label={`${skill.overrides ? 'Restore the built-in' : 'Delete'} ${skill.name}`}>
                    {skill.overrides ? 'Restore' : 'Delete'}
                  </Button>
                )}
                title={skill.overrides
                  ? `Restore the built-in ${skill.name}?`
                  : `Delete ${skill.name}?`}
                description={skill.overrides
                  ? 'Your copy is deleted and the built-in skill of this name is back, switched on.'
                  : 'Turns no longer list or load it.'}
                confirmLabel={skill.overrides ? 'Restore the built-in' : 'Delete the skill'}
                tone="destructive"
                inlineError
                finalFocus={addRef}
                onConfirm={() => actions.remove(skill.name)}
              />
            )}
          </div>
        )}
      />
      <SkillDialog
        target={target}
        onClose={() => setTarget(undefined)}
        finalFocus={openerRef}
        taken={list.data?.map(s => s.name) ?? []}
        onSave={actions.put}
      />
    </>
  )
}
