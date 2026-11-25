import type { ComponentProps } from 'react'
import { cn } from 'cn'
import { Input } from '@/components/ui/input'

// a secret is no login: a type=password field invites the browser's password manager to fill in the owner's saved
// dashboard password, or to store this secret as an update of it, and chromium ignores autocomplete=off there. so a
// text field masked with -webkit-text-security (chromium, safari, firefox 114 on), autocomplete off, and the ignore
// marks of 1password, lastpass, bitwarden and dashlane; its name, when it has one, must not read as a password
const IGNORE_MANAGERS = {
  'data-1p-ignore': '',
  'data-lpignore': 'true',
  'data-bwignore': '',
  'data-form-type': 'other',
}

// write-only and uncontrolled: the caller reads the value from the field (or its form) at submit and clears it at once,
// so it never enters react's state, a cache or an attribute
export function SecretInput({ className, ...props }: Omit<ComponentProps<'input'>, 'type' | 'value' | 'defaultValue'>) {
  return (
    <Input
      type="text"
      autoComplete="off"
      autoCorrect="off"
      autoCapitalize="off"
      spellCheck={false}
      {...IGNORE_MANAGERS}
      className={cn('[-webkit-text-security:disc]', className)}
      {...props}
    />
  )
}
