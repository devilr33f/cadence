import type { ThemePreference } from '@/lib/theme'
import { MonitorIcon, MoonIcon, SunIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { useTheme } from '@/hooks/use-theme'

const OPTIONS = [
  { value: 'system', label: 'System', icon: MonitorIcon },
  { value: 'light', label: 'Light', icon: SunIcon },
  { value: 'dark', label: 'Dark', icon: MoonIcon },
] as const

export function ThemeToggle() {
  const { preference, theme, setPreference } = useTheme()
  const Icon = theme === 'dark'
    ? MoonIcon
    : SunIcon
  const label = preference === 'system'
    ? `Theme: system (${theme})`
    : `Theme: ${preference}`

  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="icon" aria-label={label} title={label} />}>
        <Icon aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-36">
        <DropdownMenuRadioGroup value={preference} onValueChange={value => setPreference(value as ThemePreference)}>
          {OPTIONS.map(({ value, label: text, icon: OptionIcon }) => (
            <DropdownMenuRadioItem key={value} value={value}>
              <OptionIcon aria-hidden="true" />
              {text}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
