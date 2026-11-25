import { cn } from 'cn'
import * as React from 'react'

// the container is the scroll box: give it a max height and the header sticks (and a virtualizer has its viewport)
function Table({ className, containerClassName, ...props }: React.ComponentProps<'table'> & { containerClassName?: string }) {
  return (
    <div
      data-slot="table-container"
      className={cn('relative w-full overflow-auto rounded-lg border bg-card', containerClassName)}
    >
      <table
        data-slot="table"
        className={cn('w-full border-separate border-spacing-0 text-sm', className)}
        {...props}
      />
    </div>
  )
}

function TableHeader({ className, ...props }: React.ComponentProps<'thead'>) {
  return (
    <thead
      data-slot="table-header"
      className={cn('[&_th]:sticky [&_th]:top-0 [&_th]:z-10', className)}
      {...props}
    />
  )
}

function TableBody({ className, ...props }: React.ComponentProps<'tbody'>) {
  return (
    <tbody
      data-slot="table-body"
      className={cn('[&>tr:last-child>td]:border-b-0', className)}
      {...props}
    />
  )
}

function TableRow({ className, ...props }: React.ComponentProps<'tr'>) {
  return (
    <tr
      data-slot="table-row"
      className={cn(
        'transition-colors duration-150 hover:bg-muted/50 data-[state=selected]:bg-accent',
        className,
      )}
      {...props}
    />
  )
}

function TableHead({ className, ...props }: React.ComponentProps<'th'>) {
  return (
    <th
      data-slot="table-head"
      className={cn(
        'h-9 border-b bg-muted px-3 text-left align-middle text-xs font-medium whitespace-nowrap text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}

function TableCell({ className, ...props }: React.ComponentProps<'td'>) {
  return (
    <td
      data-slot="table-cell"
      className={cn('h-(--row-height) border-b px-3 align-middle whitespace-nowrap', className)}
      {...props}
    />
  )
}

export {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
}
