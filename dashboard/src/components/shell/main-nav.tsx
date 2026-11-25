import type { SectionBadge, SectionBadges } from './nav-sections'
import { Link } from '@tanstack/react-router'
import { SECTION_GROUPS, SECTION_ROUTES } from './nav-sections'

// the number on the rail is for the eye; a screen reader hears the label after the section's name
function Badge({ badge }: { badge?: SectionBadge }) {
  if (!badge || badge.count <= 0)
    return null
  return (
    <>
      <span aria-hidden="true" className="ml-auto inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-sidebar-primary px-1.5 text-xs font-semibold text-sidebar-primary-foreground tabular-nums">
        {badge.more
          ? `${badge.count}+`
          : badge.count}
      </span>
      <span className="sr-only">{`, ${badge.label}`}</span>
    </>
  )
}

export function MainNav({ badges = {}, onNavigate }: { badges?: SectionBadges, onNavigate?: () => void }) {
  const groups = SECTION_GROUPS
    .map(group => group.flatMap(section => (SECTION_ROUTES[section.id]
      ? [
          { ...section, to: SECTION_ROUTES[section.id] },
        ]
      : [])))
    .filter(group => group.length > 0)

  return (
    <nav aria-label="Main" className="flex flex-col gap-5 px-3 py-2">
      {groups.map(group => (
        <ul key={group[0]!.id} className="flex flex-col gap-0.5">
          {group.map(({ id, label, icon: Icon, to }) => (
            <li key={id}>
              <Link
                to={to}
                activeOptions={{ exact: to === '/' }}
                onClick={onNavigate}
                className="group/nav flex h-8 items-center gap-2.5 rounded-md px-2.5 text-sm font-medium text-sidebar-foreground transition-colors duration-150 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground data-[status=active]:bg-sidebar-accent data-[status=active]:text-sidebar-accent-foreground"
              >
                <Icon aria-hidden="true" className="size-4 shrink-0 opacity-80 group-data-[status=active]/nav:text-sidebar-primary group-data-[status=active]/nav:opacity-100" />
                {label}
                <Badge badge={badges[id]} />
              </Link>
            </li>
          ))}
        </ul>
      ))}
    </nav>
  )
}
