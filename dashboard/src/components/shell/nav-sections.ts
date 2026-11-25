import type { LinkProps } from '@tanstack/react-router'
import type { LucideIcon } from 'lucide-react'
import { AudioLinesIcon, BookOpenIcon, BrainIcon, ChartLineIcon, GroupIcon, HistoryIcon, KeyRoundIcon, LayersIcon, LayoutGridIcon, LibraryIcon, ListChecksIcon, PuzzleIcon, ScrollTextIcon, SettingsIcon, SlidersHorizontalIcon, SpeakerIcon, WaypointsIcon } from 'lucide-react'

export type SectionId
  = | 'overview' | 'devices' | 'groups' | 'catalog' | 'history' | 'providers' | 'modes' | 'skills' | 'wakewords' | 'memory' | 'tasks' | 'integrations' | 'logs' | 'traces' | 'metrics' | 'settings'
    | 'tokens'

export interface Section {
  id: SectionId
  label: string
  icon: LucideIcon
}

// the sections of S0 §13, in groups: the household, the assistant, the system
export const SECTION_GROUPS: readonly (readonly Section[])[] = [
  [
    { id: 'overview', label: 'Overview', icon: LayoutGridIcon },
    { id: 'devices', label: 'Devices', icon: SpeakerIcon },
    { id: 'groups', label: 'Groups', icon: GroupIcon },
    { id: 'catalog', label: 'Catalog', icon: LibraryIcon },
    { id: 'history', label: 'History', icon: HistoryIcon },
  ],
  [
    { id: 'providers', label: 'Providers', icon: LayersIcon },
    { id: 'modes', label: 'Modes', icon: SlidersHorizontalIcon },
    { id: 'skills', label: 'Skills', icon: BookOpenIcon },
    { id: 'wakewords', label: 'Wake words', icon: AudioLinesIcon },
    { id: 'memory', label: 'Memory', icon: BrainIcon },
    { id: 'tasks', label: 'Tasks', icon: ListChecksIcon },
    { id: 'integrations', label: 'Integrations', icon: PuzzleIcon },
  ],
  [
    { id: 'logs', label: 'Logs', icon: ScrollTextIcon },
    { id: 'traces', label: 'Traces', icon: WaypointsIcon },
    { id: 'metrics', label: 'Metrics', icon: ChartLineIcon },
    { id: 'settings', label: 'Settings', icon: SettingsIcon },
    { id: 'tokens', label: 'Tokens', icon: KeyRoundIcon },
  ],
]

// the feature list: a section joins the nav once its page exists, so the nav never holds a dead link
export const SECTION_ROUTES: Partial<Record<SectionId, LinkProps['to']>> = {
  overview: '/',
  devices: '/devices',
  groups: '/groups',
  catalog: '/catalog',
  history: '/history',
  providers: '/providers',
  modes: '/modes',
  skills: '/skills',
  wakewords: '/wakewords',
  memory: '/memory',
  tasks: '/tasks',
  integrations: '/integrations',
  logs: '/logs',
  traces: '/traces',
  settings: '/settings',
  tokens: '/tokens',
}

// a count beside a section (what waits for the owner there), and how a screen reader says it
export interface SectionBadge {
  count: number
  // the count is of a capped read: at least this many
  more?: boolean
  label: string
}

export type SectionBadges = Partial<Record<SectionId, SectionBadge>>
