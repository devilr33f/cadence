import { cn } from 'cn'
import * as React from 'react'

// the input's edge, surface and focus outline, for longer text
function Textarea({ className, ...props }: React.ComponentProps<'textarea'>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        'flex field-sizing-content min-h-20 w-full rounded-md border border-input bg-card px-3 py-2 text-base transition-colors duration-150 placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive',
        className,
      )}
      {...props}
    />
  )
}

export { Textarea }
