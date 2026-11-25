import { Tabs as TabsPrimitive } from '@base-ui/react/tabs'
import { cn } from 'cn'

function Tabs({ className, ...props }: TabsPrimitive.Root.Props) {
  return <TabsPrimitive.Root data-slot="tabs" className={cn('flex flex-col gap-6', className)} {...props} />
}

// a row of words over a hairline; the active one is ink, underlined in ink (the yellow is never an edge on paper)
function TabsList({ className, ...props }: TabsPrimitive.List.Props) {
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      className={cn('flex flex-wrap gap-1 border-b', className)}
      {...props}
    />
  )
}

function TabsTab({ className, ...props }: TabsPrimitive.Tab.Props) {
  return (
    <TabsPrimitive.Tab
      data-slot="tabs-tab"
      className={cn(
        '-mb-px inline-flex h-9 shrink-0 items-center rounded-t-md border-b-2 border-transparent px-3 text-sm font-medium text-muted-foreground transition-colors duration-150 ease-out hover:text-foreground data-active:border-foreground data-active:text-foreground',
        className,
      )}
      {...props}
    />
  )
}

function TabsPanel({ className, ...props }: TabsPrimitive.Panel.Props) {
  return <TabsPrimitive.Panel data-slot="tabs-panel" className={className} {...props} />
}

export { Tabs, TabsList, TabsPanel, TabsTab }
