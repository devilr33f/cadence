import type { CapabilityView } from '@/api/gen'
import { useId, useState } from 'react'
import { sendDeviceCommand } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { prettyJson } from '@/components/forms/fields/field-text'
import { JsonForm } from '@/components/forms/json-form'
import { Button } from '@/components/ui/button'
import { useRefreshDevice } from './use-refresh-device'

interface Result {
  name: string
  value: unknown
}

// a button per command; the open one takes its arguments (its schema's form, raw json when it declares none) and shows
// the device's result, kept in this card only
export function CapabilityCommands({ deviceId, capability, schemas }: {
  deviceId: string
  capability: CapabilityView
  schemas: Readonly<Record<string, unknown>>
}) {
  const api = useApi()
  const refresh = useRefreshDevice(deviceId)
  const panelId = useId()
  const [open, setOpen] = useState<string>()
  const [result, setResult] = useState<Result>()

  function toggle(name: string) {
    setResult(undefined)
    setOpen(current => current === name
      ? undefined
      : name)
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap gap-2">
        {capability.commands.map(({ name }) => (
          <Button
            key={name}
            type="button"
            size="sm"
            variant={open === name
              ? 'secondary'
              : 'outline'}
            className="font-mono text-xs"
            aria-expanded={open === name}
            aria-controls={open === name
              ? panelId
              : undefined}
            onClick={() => toggle(name)}
          >
            {name}
          </Button>
        ))}
      </div>
      {open && (
        <div id={panelId} className="flex flex-col gap-4 rounded-md border p-4">
          <JsonForm
            key={open}
            schema={schemas[open]}
            initial={{}}
            label={`Arguments of ${open}`}
            submitLabel={`Send ${open}`}
            empty={<p className="text-sm text-muted-foreground">No arguments.</p>}
            onSubmit={async (args) => {
              const { data } = await sendDeviceCommand({
                client: api,
                path: { id: deviceId, cap: capability.id, name: open },
                body: args,
                throwOnError: true,
              })
              setResult({ name: open, value: data.result })
              await refresh()
            }}
          />
          {result?.name === open && (
            <div role="status" aria-label={`Result of ${open}`} className="flex flex-col gap-1.5">
              <p className="text-sm font-medium">Result</p>
              <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 font-mono text-xs/5 break-words whitespace-pre-wrap">{prettyJson(result.value) || 'null'}</pre>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
