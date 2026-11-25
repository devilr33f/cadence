import { cva } from 'class-variance-authority'

// apart from the component so a link can look like a button (and fast refresh keeps working)
export const buttonVariants = cva(
  'group/button inline-flex shrink-0 items-center justify-center rounded-md border border-transparent bg-clip-padding text-sm font-medium whitespace-nowrap transition-[background-color,border-color,color,box-shadow] duration-150 ease-out outline-none select-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background active:not-aria-[haspopup]:translate-y-px disabled:pointer-events-none disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:opacity-50 aria-invalid:border-destructive [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*=size-])]:size-4',
  {
    variants: {
      variant: {
        default:
          'border-[color-mix(in_oklch,var(--primary),var(--primary-foreground)_22%)] bg-primary text-primary-foreground hover:bg-[color-mix(in_oklch,var(--primary),var(--primary-foreground)_10%)]',
        outline:
          'border-border bg-card text-foreground hover:bg-muted aria-expanded:bg-muted',
        secondary:
          'bg-secondary text-secondary-foreground hover:bg-[color-mix(in_oklch,var(--secondary),var(--foreground)_6%)] aria-expanded:bg-[color-mix(in_oklch,var(--secondary),var(--foreground)_6%)]',
        ghost:
          'text-foreground hover:bg-muted aria-expanded:bg-muted',
        destructive:
          'bg-destructive text-destructive-foreground hover:bg-[color-mix(in_oklch,var(--destructive),var(--foreground)_14%)]',
        link: 'h-auto px-0 text-foreground underline decoration-input underline-offset-4 hover:decoration-foreground',
      },
      size: {
        'default':
          'h-8 gap-1.5 px-3 has-data-[icon=inline-end]:pr-2.5 has-data-[icon=inline-start]:pl-2.5',
        'xs': 'h-6 gap-1 rounded-sm px-2 text-xs has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 [&_svg:not([class*=size-])]:size-3',
        'sm': 'h-7 gap-1 px-2.5 has-data-[icon=inline-end]:pr-2 has-data-[icon=inline-start]:pl-2 [&_svg:not([class*=size-])]:size-3.5',
        'lg': 'h-9 gap-2 px-4 text-base has-data-[icon=inline-end]:pr-3 has-data-[icon=inline-start]:pl-3',
        'icon': 'size-8',
        'icon-xs': 'size-6 rounded-sm [&_svg:not([class*=size-])]:size-3',
        'icon-sm': 'size-7',
        'icon-lg': 'size-9',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  },
)
