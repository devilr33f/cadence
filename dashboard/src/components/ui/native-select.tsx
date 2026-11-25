import { cn } from 'cn'
import { ChevronDownIcon } from 'lucide-react'
import * as React from 'react'

type NativeSelectProps = Omit<React.ComponentProps<'select'>, 'size'> & {
  size?: 'sm' | 'default'
}

// the browser's own select, with the input's 36px, edge, surface and focus outline
function NativeSelect({
  className,
  size = 'default',
  ...props
}: NativeSelectProps) {
  return (
    <div
      className={cn(
        'group/native-select relative w-fit has-[select:disabled]:opacity-50',
        className,
      )}
      data-slot="native-select-wrapper"
      data-size={size}
    >
      <select
        data-slot="native-select"
        data-size={size}
        className="h-9 w-full min-w-0 appearance-none rounded-md border border-input bg-card py-1 pr-8 pl-3 text-base transition-colors duration-150 select-none disabled:pointer-events-none disabled:cursor-not-allowed aria-invalid:border-destructive data-[size=sm]:h-7 data-[size=sm]:text-sm"
        {...props}
      />
      <ChevronDownIcon
        className="pointer-events-none absolute top-1/2 right-2.5 size-4 -translate-y-1/2 text-muted-foreground select-none"
        aria-hidden="true"
        data-slot="native-select-icon"
      />
    </div>
  )
}

function NativeSelectOption({
  className,
  ...props
}: React.ComponentProps<'option'>) {
  return (
    <option
      data-slot="native-select-option"
      className={cn('bg-[Canvas] text-[CanvasText]', className)}
      {...props}
    />
  )
}

export { NativeSelect, NativeSelectOption }
