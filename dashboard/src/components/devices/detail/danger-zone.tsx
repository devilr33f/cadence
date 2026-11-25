import type { Detail } from '@/api/gen'
import { Trash2Icon } from 'lucide-react'
import { useId } from 'react'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { Button } from '@/components/ui/button'

// the page's last section: deleting the device is asked for with its name typed, and worded in the dialog when refused
export function DangerZone({ device, onDelete }: { device: Detail, onDelete: () => Promise<void> }) {
  const titleId = useId()
  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-3">
      <h2 id={titleId} className="text-lg font-semibold">Danger zone</h2>
      <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 rounded-lg border bg-card p-4">
        <div className="flex max-w-[65ch] flex-col gap-1">
          <p className="font-medium">Delete this speaker</p>
          <p className="text-sm text-muted-foreground">
            Its token, settings, capabilities with their secrets, its own memories and its conversations are deleted at
            once, and its link closes; its logs and metrics age out. To use it again, enroll it as a new speaker.
          </p>
        </div>
        <ConfirmDialog
          trigger={(
            <Button type="button" variant="outline" className="text-destructive">
              <Trash2Icon aria-hidden="true" />
              Delete this speaker
            </Button>
          )}
          title={`Delete ${device.name}?`}
          description="This cannot be undone: the speaker stops working until it is enrolled again, as a new device."
          confirmLabel="Delete the speaker"
          tone="destructive"
          typeToConfirm={device.name}
          inlineError
          onConfirm={onDelete}
        />
      </div>
    </section>
  )
}
