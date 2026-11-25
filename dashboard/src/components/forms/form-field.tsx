import type { ReactNode } from 'react'
import { cn } from 'cn'
import { Label } from '@/components/ui/label'
import { FieldMessage, RequiredMark } from './fields/field-shell'

// a hand-made form's field, dressed as the schema renderer's are: the label above (an id's in mono), its required mark,
// a description, the control, and the message (`${id}-error`) that names what is wrong
export function FormField({ id, label, required = false, mono = false, description, error, children }: {
  id: string
  label: string
  required?: boolean
  mono?: boolean
  description?: ReactNode
  error?: string
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id} className={cn(mono && 'font-mono')}>
        {label}
        <RequiredMark required={required} />
      </Label>
      {description !== undefined && <p id={`${id}-description`} className="-mt-1 text-sm text-muted-foreground">{description}</p>}
      {children}
      <FieldMessage id={id} error={error} />
    </div>
  )
}
