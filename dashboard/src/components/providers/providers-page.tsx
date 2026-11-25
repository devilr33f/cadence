import type { ProviderRow } from './provider-rows'
import type { Instance } from '@/api/gen'
import { useQuery } from '@tanstack/react-query'
import { LayersIcon, PlusIcon } from 'lucide-react'
import { useMemo, useRef, useState } from 'react'
import { useApi } from '@/api/use-api'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { DataTable } from '@/components/data/data-table'
import { PageHeader } from '@/components/data/page-header'
import { EmptyState, ErrorState } from '@/components/data/states'
import { Button } from '@/components/ui/button'
import { providerModulesQueryOptions, providersQueryOptions, useProviderActions } from '@/lib/providers'
import { useTimeZone } from '@/lib/time-zone'
import { CreateProviderDialog } from './create-provider-dialog'
import { EditProviderDialog } from './edit-provider-dialog'
import { catalogIndex } from './provider-modules'
import { sortProviders } from './provider-rows'
import { providerColumns } from './provider-table'
import { ProviderTestButton } from './provider-test'
import { useProviderTests } from './use-provider-tests'

export function ProvidersPage() {
  const api = useApi()
  const timeZone = useTimeZone()
  const list = useQuery(providersQueryOptions(api))
  const catalog = useQuery(providerModulesQueryOptions(api))
  const actions = useProviderActions()
  const { tests, run } = useProviderTests(actions.test)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Instance>()
  // the page's action takes the focus back from every dialog: a row's delete button, or the empty state's add, is gone
  // by the time it closes
  const addRef = useRef<HTMLButtonElement>(null)
  const columns = useMemo(() => providerColumns(timeZone, tests), [timeZone, tests])
  // a row's kind and secrets come from the catalog, so the list shows once both are read
  const rows: ProviderRow[] | undefined = useMemo(() => {
    if (!list.data || !catalog.data)
      return undefined
    const modules = catalogIndex(catalog.data)
    return sortProviders(list.data.map(instance => ({
      instance,
      module: modules.get(instance.module),
    })))
  }, [list.data, catalog.data])
  const failed = list.isError
    ? list
    : catalog.isError ? catalog : undefined
  // the dialog's modules come from the catalog: until it is read there would be none to choose (the table says why)
  const catalogRead = catalog.data !== undefined

  const add = (size: 'default' | 'lg') => (
    <Button
      ref={size === 'lg'
        ? addRef
        : undefined}
      size={size}
      aria-disabled={!catalogRead || undefined}
      onClick={() => {
        if (catalogRead)
          setCreating(true)
      }}
    >
      <PlusIcon aria-hidden="true" />
      Add a provider
    </Button>
  )

  return (
    <>
      <PageHeader
        title="Providers"
        description="The services the assistant runs on: speech, voice, models, weather, search, embeddings and the integrations. Settings name an instance to use it."
        actions={add('lg')}
      />
      <DataTable
        caption="Providers"
        columns={columns}
        rows={rows}
        rowKey={r => r.instance.id}
        status={failed
          ? 'error'
          : rows ? 'ready' : 'loading'}
        error={(
          <ErrorState
            title="The providers could not be listed"
            error={failed?.error}
            onRetry={() => {
              for (const query of [list, catalog]) {
                if (query.isError)
                  void query.refetch()
              }
            }}
          />
        )}
        empty={(
          <EmptyState
            icon={LayersIcon}
            title="No providers yet"
            description="Add an instance of a module, such as Deepgram for speech or OpenRouter for the model, with its key; then name it in the settings."
            action={add('default')}
          />
        )}
        rowActions={({ instance, module }) => (
          <div className="flex justify-end gap-1">
            {module && <Button type="button" variant="ghost" size="sm" aria-label={`Edit ${instance.name}`} onClick={() => setEditing(instance)}>Edit</Button>}
            <ProviderTestButton name={instance.name} test={tests[instance.id]} onRun={() => void run(instance)} />
            <ConfirmDialog
              trigger={<Button type="button" variant="ghost" size="sm" aria-label={`Delete ${instance.name}`}>Delete</Button>}
              title={`Delete ${instance.name}?`}
              description="Its secrets go with it. The server refuses while a setting names it (in_use): name another instance there first."
              confirmLabel="Delete the provider"
              tone="destructive"
              inlineError
              finalFocus={addRef}
              onConfirm={() => actions.remove(instance.name)}
            />
          </div>
        )}
      />
      <CreateProviderDialog
        open={creating}
        onOpenChange={setCreating}
        finalFocus={addRef}
        modules={catalog.data ?? []}
        onCreate={actions.create}
      />
      {editing && (
        <EditProviderDialog
          instance={editing}
          onOpenChange={(open) => {
            if (!open)
              setEditing(undefined)
          }}
          finalFocus={addRef}
          modules={catalog.data ?? []}
          onUpdate={actions.update}
        />
      )}
    </>
  )
}
