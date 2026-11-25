import type { SectionBadges } from './nav-sections'
import { MenuIcon } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from '@/components/ui/sheet'
import { MainNav } from './main-nav'
import { Wordmark } from './wordmark'

// below the tablet breakpoint the rail folds into a panel the header opens
export function MobileNav({ badges }: { badges?: SectionBadges }) {
  const [open, setOpen] = useState(false)
  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger render={<Button variant="ghost" size="icon" aria-label="Open navigation" className="md:hidden" />}>
        <MenuIcon aria-hidden="true" />
      </SheetTrigger>
      <SheetContent
        side="left"
        className="w-(--rail-width) gap-0 border-sidebar-border bg-sidebar text-sidebar-foreground [--ring:var(--sidebar-ring)]"
        closeClassName="text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
      >
        <SheetTitle className="sr-only">Navigation</SheetTitle>
        <div className="flex h-(--header-height) items-center px-5 text-sidebar-accent-foreground">
          <Wordmark />
        </div>
        <MainNav badges={badges} onNavigate={() => setOpen(false)} />
      </SheetContent>
    </Sheet>
  )
}
