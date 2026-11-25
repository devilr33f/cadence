import type { ReactElement, ReactNode, RefObject } from 'react'
import { Loader2Icon } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { describeError } from '@/api/describe-error'
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

export interface ConfirmDialogProps {
  trigger: ReactElement
  title: string
  description: ReactNode
  confirmLabel: string
  tone?: 'default' | 'destructive'
  // rejects to keep the dialog open: the caller reports why (a toast), or the dialog words it inside (inlineError). it
  // gets the version the dialog opened on
  onConfirm: (opened?: string) => Promise<void>
  // the refusal is worded in the dialog, in codes, where the person who tried can read it and try again
  inlineError?: boolean
  // where the focus goes once a confirmed action closed it, when the trigger may be gone by then (a delete removes its
  // own button); a cancel gives it back to the trigger
  finalFocus?: RefObject<HTMLElement | null>
  // the version of what it acts on (a tool's hash, a question's token): the dialog keeps the description it opened
  // with, and once the page reads a newer version under it, the confirm is blocked until it is opened again, so
  // nothing the person did not see is ever answered
  version?: string
  // what must be typed, exactly, before the confirm runs (a device's name before it is deleted); the dialog opens on
  // its field
  typeToConfirm?: string
}

interface Opened {
  version?: string
  description: ReactNode
}

const CHANGED_TEXT = 'Changed since you opened this: close it and open it again to see what it is now.'

export function ConfirmDialog({ trigger, title, description, confirmLabel, tone = 'default', onConfirm, inlineError = false, finalFocus, version, typeToConfirm }: ConfirmDialogProps) {
  const [open, setOpen] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const [opened, setOpened] = useState<Opened>()
  const [typed, setTyped] = useState('')
  const [mistyped, setMistyped] = useState(false)
  const confirmedRef = useRef(false)
  const typedRef = useRef<HTMLInputElement>(null)
  const typedId = useId()
  const versioned = version !== undefined
  const changed = versioned && opened !== undefined && opened.version !== version
  const untyped = typeToConfirm !== undefined && typed.trim() !== typeToConfirm

  async function confirm() {
    // never disabled under the focus: a click while it runs, or on a changed version, does nothing
    if (pending || changed)
      return
    if (untyped) {
      setMistyped(true)
      typedRef.current?.focus()
      return
    }
    setPending(true)
    setError(undefined)
    try {
      await onConfirm(opened?.version)
      confirmedRef.current = true
      setOpen(false)
    }
    catch (failure) {
      // the dialog stays open for another try
      if (inlineError)
        setError(describeError(failure))
    }
    finally {
      setPending(false)
    }
  }

  const mistypedShown = mistyped && untyped
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (pending)
          return
        if (next) {
          confirmedRef.current = false
          setOpened({ version, description })
          setTyped('')
          setMistyped(false)
        }
        setOpen(next)
        setError(undefined)
      }}
    >
      <AlertDialogTrigger render={trigger} />
      <AlertDialogContent finalFocus={finalFocus && (() => confirmedRef.current ? finalFocus.current : true)}>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>
            {versioned && opened
              ? opened.description
              : description}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {typeToConfirm !== undefined && (
          <div className="flex flex-col gap-2">
            <Label htmlFor={typedId}>{`Type ${typeToConfirm} to confirm`}</Label>
            <Input
              ref={typedRef}
              id={typedId}
              value={typed}
              autoComplete="off"
              spellCheck={false}
              aria-invalid={mistypedShown || undefined}
              aria-describedby={mistypedShown
                ? `${typedId}-error`
                : undefined}
              onChange={e => setTyped(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  void confirm()
                }
              }}
            />
            {mistypedShown && <p id={`${typedId}-error`} role="alert" className="text-sm text-destructive">{`Type ${typeToConfirm} exactly to confirm.`}</p>}
          </div>
        )}
        {changed && <p role="status" className="text-sm font-medium text-warning">{CHANGED_TEXT}</p>}
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
          <Button
            variant={tone === 'destructive'
              ? 'destructive'
              : 'default'}
            aria-disabled={pending || changed || untyped || undefined}
            onClick={() => void confirm()}
          >
            {pending && <Loader2Icon aria-hidden="true" className="animate-spin" />}
            {confirmLabel}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
