import type { ReactNode } from 'react'
import type { FieldNode } from '../json-schema'
import { Label } from '@/components/ui/label'
import { fieldLabel } from './field-text'

export function RequiredMark({ required }: { required: boolean }) {
  if (!required)
    return null
  return (
    <>
      <span aria-hidden="true" className="-ml-1 text-destructive">*</span>
      {' '}
      <span className="sr-only">(required)</span>
    </>
  )
}

export function FieldMessage({ id, error }: { id: string, error?: string }) {
  if (!error)
    return null
  return <p id={`${id}-error`} role="alert" className="text-sm text-destructive">{error}</p>
}

// what every field is dressed in: its label above (the required mark beside it), a description, the control, why it is wrong
export function FieldShell({ node, id, error, labelId, children, action }: {
  node: FieldNode
  id: string
  error?: string
  // a control with no labelable element (a switch) is named by the label's id instead
  labelId?: string
  action?: ReactNode
  children: ReactNode
}) {
  const description = typeof node.schema.description === 'string'
    ? node.schema.description
    : undefined
  return (
    <div className="flex flex-col gap-2">
      <div className="flex min-h-5 items-center justify-between gap-3">
        {/* a key without a title is an id, set as one */}
        <Label
          id={labelId}
          htmlFor={labelId ? undefined : id}
          className={typeof node.schema.title === 'string'
            ? undefined
            : 'font-mono'}
        >
          {fieldLabel(node)}
          <RequiredMark required={node.required} />
        </Label>
        {action}
      </div>
      {description && <p id={`${id}-description`} className="-mt-1 text-sm text-muted-foreground">{description}</p>}
      {children}
      <FieldMessage id={id} error={error} />
    </div>
  )
}
