import type { CapabilityView } from '@/api/gen'
import { PencilIcon } from 'lucide-react'
import { useState } from 'react'
import { putDeviceConfig } from '@/api/gen'
import { useApi } from '@/api/use-api'
import { KeyValueList } from '@/components/data/key-value-list'
import { JsonForm } from '@/components/forms/json-form'
import { isObject } from '@/components/forms/json-pointer'
import { Button } from '@/components/ui/button'
import { useFocusReturn } from '@/hooks/use-focus-return'
import { jsonItems } from './json-items'
import { useRefreshDevice } from './use-refresh-device'

// the desired config: read as a list, edited through the schema's form (raw json while there is no schema); a save is a
// new generation the badge then follows
export function CapabilityConfig({ deviceId, capability, schema }: {
  deviceId: string
  capability: CapabilityView
  schema?: unknown
}) {
  const api = useApi()
  const refresh = useRefreshDevice(deviceId)
  const [editing, setEditing] = useState(false)
  const back = useFocusReturn<HTMLButtonElement>(editing)
  const close = () => {
    back.arm()
    setEditing(false)
  }
  const desired = isObject(capability.desired)
    ? capability.desired
    : {}

  if (editing) {
    return (
      // opened on the desired config of the moment; a generation arriving meanwhile (a secret set) leaves the draft alone
      <JsonForm
        autoFocus
        schema={schema}
        initial={desired}
        label={`Configuration of ${capability.id}`}
        submitLabel="Save the configuration"
        onCancel={close}
        onSubmit={async (value) => {
          await putDeviceConfig({
            client: api,
            path: { id: deviceId, cap: capability.id },
            body: value,
            throwOnError: true,
          })
          close()
          await refresh()
        }}
      />
    )
  }

  return (
    <div className="flex flex-col items-start gap-3">
      {Object.keys(desired).length === 0
        ? <p className="text-sm text-muted-foreground">Nothing set: the device&apos;s defaults apply.</p>
        : <KeyValueList items={jsonItems(desired)} className="w-full" />}
      <Button ref={back.ref} type="button" variant="outline" size="sm" onClick={() => setEditing(true)}>
        <PencilIcon aria-hidden="true" />
        Edit the configuration
      </Button>
    </div>
  )
}
