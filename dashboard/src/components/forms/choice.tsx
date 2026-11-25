import type { ReactNode } from 'react'

// one checkbox or radio of a hand-made form, its label beside it and what it means below the label
export function Choice({ id, type = 'checkbox', name, checked, onChange, label, description, disabled }: {
  id: string
  type?: 'checkbox' | 'radio'
  name?: string
  checked: boolean
  onChange: (checked: boolean) => void
  label: ReactNode
  description?: ReactNode
  disabled?: boolean
}) {
  return (
    <div className={disabled ? 'flex items-start gap-2.5 opacity-60' : 'flex items-start gap-2.5'}>
      <input
        id={id}
        type={type}
        name={name}
        className="mt-0.5 size-4 shrink-0"
        checked={checked}
        disabled={disabled}
        aria-describedby={description === undefined
          ? undefined
          : `${id}-description`}
        onChange={e => onChange(e.target.checked)}
      />
      <div className="flex flex-col gap-0.5">
        <label htmlFor={id} className="text-sm font-medium">{label}</label>
        {description !== undefined && <p id={`${id}-description`} className="text-sm text-muted-foreground">{description}</p>}
      </div>
    </div>
  )
}
