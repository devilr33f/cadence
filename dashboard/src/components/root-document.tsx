import type { ReactNode } from 'react'
import { HeadContent, Scripts } from '@tanstack/react-router'

export function RootDocument({ children }: { children: ReactNode }) {
  return (
    // the theme script sets the root's class before react hydrates
    <html lang="en" suppressHydrationWarning>
      <head>
        <HeadContent />
      </head>
      <body>
        {children}
        <Scripts />
      </body>
    </html>
  )
}
