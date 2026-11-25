import type { AccountState } from './shell-state'
import { LogOutIcon, UserRoundIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'

export function AccountStatus({ account }: { account: AccountState }) {
  if (account.state === 'signed-out')
    return <span className="text-sm text-muted-foreground">Not signed in</span>

  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="sm" />}>
        <UserRoundIcon aria-hidden="true" />
        Owner
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-40">
        <DropdownMenuItem onClick={account.onSignOut}>
          <LogOutIcon aria-hidden="true" />
          Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
