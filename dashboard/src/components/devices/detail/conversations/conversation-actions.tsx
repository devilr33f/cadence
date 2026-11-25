import { Loader2Icon, RotateCcwIcon } from 'lucide-react'
import { useState } from 'react'
import { describeError } from '@/api/describe-error'
import { ConfirmDialog } from '@/components/data/confirm-dialog'
import { Button } from '@/components/ui/button'
import { toast } from '@/components/ui/toast-manager'
import { useConversationActions } from '@/lib/conversations'

// reset moves the context boundary; compact only summarizes. both leave the stored turns and long-term memories alone
export function ConversationActions({ deviceId }: { deviceId: string }) {
  const actions = useConversationActions(deviceId)
  const [pending, setPending] = useState(false)
  const [refusal, setRefusal] = useState<string>()

  async function compact() {
    if (pending)
      return
    setPending(true)
    setRefusal(undefined)
    try {
      await actions.compact()
      toast.add({ type: 'info', title: 'Compaction started', description: 'The summary and memory learning will follow when it finishes.' })
    }
    catch (error) {
      setRefusal(`The conversation was not compacted. ${describeError(error)}`)
    }
    finally {
      setPending(false)
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      {refusal && <p role="alert" className="mr-2 text-sm text-destructive">{refusal}</p>}
      <Button type="button" variant="outline" size="sm" aria-disabled={pending || undefined} onClick={() => void compact()}>
        {pending && <Loader2Icon aria-hidden="true" className="motion-safe:animate-spin" />}
        Compact now
      </Button>
      <ConfirmDialog
        trigger={(
          <Button type="button" variant="outline" size="sm">
            <RotateCcwIcon aria-hidden="true" />
            Reset conversation
          </Button>
        )}
        title="Reset this conversation?"
        description="The next turn starts with no earlier turns or summary. The stored history stays for 14 days, and long-term memories are not deleted. The closed segment is compacted and learned from in the background."
        confirmLabel="Reset conversation"
        inlineError
        onConfirm={async () => {
          await actions.reset()
          toast.add({ type: 'success', title: 'Conversation reset', description: 'The next turn starts fresh.' })
        }}
      />
    </div>
  )
}
