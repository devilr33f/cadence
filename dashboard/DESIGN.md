---
name: Cadence dashboard
description: The owner's console for a household of voice speakers, set like a station timetable.
colors:
  ink: "oklch(0.215 0.022 262)"
  paper: "oklch(0.985 0.003 255)"
  paper-card: "oklch(1 0 0)"
  night: "oklch(0.17 0.014 262)"
  night-card: "oklch(0.205 0.016 262)"
  night-ink: "oklch(0.945 0.006 255)"
  departure-yellow: "oklch(0.87 0.165 92)"
  highlight: "oklch(0.955 0.055 96)"
  highlight-night: "oklch(0.29 0.04 95)"
  rail: "oklch(0.24 0.024 262)"
  rail-night: "oklch(0.135 0.012 262)"
  success: "oklch(0.5 0.12 152)"
  warning: "oklch(0.53 0.12 62)"
  danger: "oklch(0.53 0.19 27)"
  info: "oklch(0.5 0.13 255)"
typography:
  page-title:
    fontFamily: "Geist Variable, ui-sans-serif, system-ui, sans-serif"
    fontSize: "1.25rem"
    fontWeight: 600
    lineHeight: 1.4
    letterSpacing: "-0.01em"
  section-title:
    fontFamily: "Geist Variable, ui-sans-serif, system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 600
    lineHeight: 1.5
  body:
    fontFamily: "Geist Variable, ui-sans-serif, system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.57
  ui:
    fontFamily: "Geist Variable, ui-sans-serif, system-ui, sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.54
  label:
    fontFamily: "Geist Variable, ui-sans-serif, system-ui, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 500
    lineHeight: 1.33
  time:
    fontFamily: "Geist Variable, ui-sans-serif, system-ui, sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 600
    lineHeight: 1.54
  mono:
    fontFamily: "Geist Mono Variable, ui-monospace, Menlo, Consolas, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: 1.67
rounded:
  sm: "4.8px"
  md: "6.4px"
  lg: "8px"
  xl: "11.2px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "12px"
  lg: "16px"
  xl: "24px"
  2xl: "32px"
components:
  button-primary:
    backgroundColor: "{colors.departure-yellow}"
    textColor: "{colors.ink}"
    rounded: "{rounded.md}"
    height: "32px"
    padding: "0 12px"
  button-primary-large:
    backgroundColor: "{colors.departure-yellow}"
    textColor: "{colors.ink}"
    rounded: "{rounded.md}"
    height: "36px"
    padding: "0 16px"
  nav-item-active:
    backgroundColor: "oklch(0.31 0.026 262)"
    textColor: "oklch(0.98 0.003 255)"
    rounded: "{rounded.md}"
    height: "32px"
  table-row:
    height: "36px"
    padding: "0 12px"
    typography: "{typography.ui}"
  table-head:
    height: "36px"
    typography: "{typography.label}"
  status-pill:
    rounded: "9999px"
    height: "24px"
    padding: "0 10px"
  live-tally:
    backgroundColor: "{colors.departure-yellow}"
    textColor: "{colors.ink}"
    rounded: "9999px"
    height: "24px"
---

# Design System: Cadence dashboard

## Overview

**The timetable.** Cadence reads like a station's departure board: every speaker, change and event is a row in a strict
column rhythm, times are set in bold tabular figures down the left, and the one thing that moves (the live feed, the
newest event, the primary action) wears the yellow of a departure poster. The rail is the board itself, dark in both
themes. It refuses the category default (a grey admin with cards of headline numbers): the owner comes to see what each
speaker is doing and whether a change arrived, so rows and states lead and nothing is decorative.

Who it is for (inferred from the plan and the spec, no interview ran): the one owner of a self-hosted household of voice
speakers, at home on a laptop or a tablet, often in the evening. Visits are short and focused: enroll a speaker, change
a capability and watch it apply, read why a turn failed, approve a task. The mode is **Operate**: familiar controls, a
standard shell, and the world enters only through type, palette, density and one signature move.

**Signature move:** a device's activity is a route's stop list (`Timeline`), and a configuration change is a journey
from the generation in force to the desired one (`ApplyBadge`: `gen 14 → 15`). A failure stays on the board, struck
through with the device's code, as a cancelled train stays on the timetable.

Character (binding, from the brief): calm; dense where data is dense (tables, logs, timelines), generous where actions
happen (forms, confirmations); one accent; no hero sections and no decorative gradients; a state never by colour alone.

The preview for the owner is `design-preview/index.html`: the real components and tokens rendered with synthetic data
into one self-contained file (Devices, Device detail, States; light and dark). After a design change, regenerate it with
`pnpm preview:build` and commit the file. It is not part of the app's build, and nothing checks it for drift.

## Colors

All colours live in `src/styles/tokens.css` as `light-dark(light, dark)` pairs; `src/styles/theme.css` maps them to
Tailwind (`bg-card`, `text-success`, `bg-warning-surface`, `bg-sidebar`…). They are normative there; the frontmatter
above names the main ones.

**Strategy: restrained, with a committed shell.** Cool paper and ink, a dark rail, and one accent.

- **Departure yellow** (`--primary`, the one accent): the primary action, the active nav item's icon, the live tally,
  the newest stop on a timeline, focus rings in dark mode and on the rail. Text on it is ink, never white. A light-theme
  yellow button carries a 1px darker edge (`color-mix` with ink), since yellow on paper has no edge of its own.
  **The yellow is about 1.4:1 on the light page and card:** it is only ever a filled surface carrying ink
  (`bg-primary text-primary-foreground`) or a mark on the dark rail (`sidebar-primary`), never bare text, an icon, a
  border, an outline, a ring or a stroke on a light surface. `tokens.test.ts` pins that ratio and
  `accent-usage.test.ts` refuses `text-`, `border-`, `outline-`, `ring-`, `fill-`, `stroke-`, `decoration-`,
  `divide-`, `caret-` and `placeholder-primary` anywhere in `src`.
- **Highlight** (`--accent`): a pale wash of the same yellow for selected rows, a desired value that differs from the
  applied one, menu items under the keyboard and text selection. It is shadcn's `accent` slot; the brand accent is
  `primary`.
- **Ink and paper** (`--foreground`, `--background`, `--card`, `--muted`, `--muted-foreground`, `--border`): cool
  neutrals at hue 255–262. The page is a shade off white, cards and tables white; in dark mode the page is the night
  board and cards sit a step lighter.
- **State** (`--success`, `--warning`, `--destructive`, `--info`, each with a `-surface` for pills): green, amber,
  red, blue. Usual states (success, neutral) keep their words in ink and colour only the glyph; warning, failure and
  progress are worded in colour. `--destructive-foreground` is white on light red, ink on dark red.
- **The rail** (`--sidebar-*`): dark in both themes; its focus ring is the yellow (`--ring` is redefined inside it).
- **Data series** (`--chart-1…6`): blue, teal, orange, violet, green, magenta, at least 3:1 on the page and on a card.
  The yellow is never a series. `--chart-1` is the one series every chart draws today; its dark step sits inside the
  dataviz validator's dark lightness band (L 0.48–0.67, checked with `validate_palette.js`), so a line does not glow on
  the night board. The other five keep their first steps until a chart draws several series; retune and validate them
  then.
- **Borders and inputs:** `--border` is a hairline for structure; `--input` reaches 3:1 because an input's edge is how
  it is found.

**Theme.** The system's preference applies with no script (`color-scheme: light dark` on the root). A choice (System,
Light, Dark in the header) is stored in `localStorage` as `cadence-theme` and applied as `.light`/`.dark` on `<html>`;
an inline script in the document head (`THEME_SCRIPT`, `src/lib/theme.ts`) sets the class before the first paint. The
build polyfills `light-dark()` at the root, so a nested `color-scheme` does not switch tokens: theme the whole page.

**The head script and a CSP.** The theme script is inline, as are TanStack Start's own hydration scripts. No
Content-Security-Policy is sent today; one that lands must let them run (`script-src` with a per-response nonce, or
the theme script's sha256). The hook for a nonce is the root route's `head().scripts` entry in `src/routes/__root.tsx`:
give it the nonce attribute there, never `'unsafe-inline'`.

**Contrast (WCAG 2.2 AA).** `src/styles/tokens.test.ts` computes every pair below from `tokens.css` (oklch to sRGB) and
fails the gate under 4.5:1 for text or 3:1 for control edges, focus rings and graphics. Ratios as built:

| Pair | Kind (minimum) | Light | Dark |
| --- | --- | --- | --- |
| `foreground` on `background` | text (4.5) | 16.8 | 16.3 |
| `foreground` on `card` | text (4.5) | 17.5 | 15.3 |
| `foreground` on `muted` | text (4.5) | 15.4 | 14.2 |
| `foreground` on `accent` | text (4.5) | 15.4 | 12.0 |
| `muted-foreground` on `background` | text (4.5) | 5.5 | 8.0 |
| `muted-foreground` on `card` | text (4.5) | 5.7 | 7.5 |
| `muted-foreground` on `muted` | text (4.5) | 5.0 | 7.0 |
| `muted-foreground` on `accent` | text (4.5) | 5.1 | 5.9 |
| `primary-foreground` on `primary` | text (4.5) | 11.8 | 11.8 |
| `destructive-foreground` on `destructive` | text (4.5) | 5.8 | 6.6 |
| `success` on `card` | text (4.5) | 5.7 | 8.8 |
| `success` on `success-surface` | text (4.5) | 5.1 | 7.3 |
| `warning` on `card` | text (4.5) | 5.5 | 9.4 |
| `warning` on `warning-surface` | text (4.5) | 4.9 | 7.7 |
| `destructive` on `card` | text (4.5) | 5.8 | 6.5 |
| `destructive` on `destructive-surface` | text (4.5) | 5.1 | 5.6 |
| `info` on `card` | text (4.5) | 6.0 | 8.1 |
| `info` on `info-surface` | text (4.5) | 5.4 | 6.8 |
| `sidebar-foreground` on `sidebar` | text (4.5) | 9.8 | 10.7 |
| `sidebar-accent-foreground` on `sidebar-accent` | text (4.5) | 12.4 | 16.0 |
| `sidebar-primary` on `sidebar-accent` | text (4.5) | 8.9 | 11.4 |
| `input` on `background` | ui (3) | 3.2 | 3.5 |
| `ring` on `background` | ui (3) | 16.8 | 12.9 |
| `sidebar-ring` on `sidebar` | ui (3) | 11.1 | 13.5 |
| `chart-1` on `card` | ui (3) | 4.9 | 5.7 |
| `chart-2` on `card` | ui (3) | 4.1 | 8.7 |
| `chart-3` on `card` | ui (3) | 3.7 | 8.7 |
| `chart-4` on `card` | ui (3) | 5.9 | 6.9 |
| `chart-5` on `card` | ui (3) | 4.1 | 8.8 |
| `chart-6` on `card` | ui (3) | 5.1 | 6.7 |

The test checks more pairs than the table shows (on `popover`, `secondary`, the page, every surface). Yellow on paper is
1.4:1 and is never text or a lone edge in the light theme.

## Typography

One family: **Geist Variable** for everything a person reads, **Geist Mono Variable** for ids, codes, hashes, model
names, capability ids and logs. Both are bundled from `@fontsource-variable/*` (Latin and Cyrillic subsets, so Russian
speaker names set in the same face); nothing loads from a CDN. `src/styles/fonts.css` declares the faces with those two
subsets' files only (the variable packages ship no per-subset stylesheet); any other character falls back to the
system face.

A fixed rem scale, dense and tight (ratio about 1.1–1.25), set in `theme.css` (Tailwind's names, Cadence's sizes):

| Class | Size / line | Use |
| --- | --- | --- |
| `text-2xl` | 24 / 32 | a rare headline number |
| `text-xl` | 20 / 28, semibold | page title (`h1`) |
| `text-lg` | 16 / 24, semibold | section and card titles, empty and error state titles |
| `text-base` | 14 / 22 | body, descriptions, forms (the `body` default) |
| `text-sm` | 13 / 20 | tables, lists, nav, controls, badges |
| `text-xs` | 12 / 16, medium | table headings, captions; `font-mono text-xs` for codes and logs |

**Times are the timetable's figures:** `font-semibold` with tabular numerals (`time`, `td` and `[data-numeric]` get
`tabular-nums` by default), so they line up down a column. Times are worded in the household's zone (the `timeZone`
setting, `formatClock` in `src/lib/format-time.ts`) so the ssr server (UTC) and the browser (its own zone) print the
same text: `timeZone` is a required argument of `formatClock` and a required prop of `Timeline`, `LogView` and
`LogLine`. Pages pass `useTimeZone()`, which is `null` until the setting loads: the times then show `--:--:--`
(`clockText`) on the ssr server and in the browser alike. Headings balance (`text-wrap: balance`) and track at -0.01em;
prose stays under 65ch.

## Layout

- **Shell** (`src/components/shell/app-shell.tsx`): the rail on the left, 15rem (`--rail-width`), from the `md`
  breakpoint (768px); below it the rail folds into a panel the header's menu button opens (`MobileNav`, a Base UI
  dialog). The header is 3.25rem (`--header-height`), sticky, opaque, across the content column: the live-feed tally,
  the signed-in state and the theme menu on the right. On a phone the header keeps only the mark of the wordmark.
- **Content:** up to 90rem (`--content-max`), 16px side gutters on a phone and 32px from `md`.
- **Rhythm:** a 4px base (Tailwind's spacing): 4, 8, 12, 16, 24, 32. Tight inside a group, 24–32 between sections, more
  space above a heading than below it. A page is a `PageHeader` (or a back link and a title), then sections titled
  `text-lg`.
- **Density:** table rows are 36px (`--row-height`), cells 12px apart; forms and dialogs breathe (36px controls, 24px
  dialog padding).
- **Responsive tables:** columns hide by priority (`className: 'max-lg:hidden'` on a column), and what remains scrolls
  inside the table's own container, never the page. A two-column detail page (main + 20rem side) stacks below `lg`.
- **Keyboard:** a skip link to `#main`, every control reachable in order, one visible focus ring everywhere (2px solid
  `--ring`, offset 2px: ink on paper, yellow on the night board and on the rail).

## Elevation & Depth

Structure is drawn with hairline borders, not shadows: tables, cards and the log are bordered surfaces on the page.
Shadows belong only to layers that float above the page and need to separate from it: menus (shadcn's), toasts (a soft
8px/24px ink shadow) and dialogs (16px/48px). The scrim behind a dialog or the nav panel is ink at 45%, never blurred.
One elevation per element: a bordered card never also casts a shadow.

## Shapes

`--radius` is 8px. Controls use `rounded-md` (6.4px), cards, tables and the log `rounded-lg` (8px), dialogs `rounded-xl`.
Full pills only for small state: status pills, the live tally, the rail's counts. A label on a row (a token's scope, a
memory's scope) is a `Tag` (`data/tag.tsx`): bordered, `rounded-md` like a control, mono when it is an id. The status
glyphs are one 10px form at every size:

| Glyph | Shape | Tone |
| --- | --- | --- |
| dot | filled circle | success (online, applied, playing) |
| ring | hollow circle | neutral (offline, debug, not connected) |
| half | half-filled circle | info (updating, pending, connecting) |
| dashed | dashed circle | enrolling |
| triangle | filled triangle | warning |
| square | filled square | danger (failed, error) |

Icons are Lucide, one stroke weight, 16px in controls and 14px in dense places.

## Components

Ours live in `src/components/shell` and `src/components/data`; the vendored shadcn ones (Base UI) in
`src/components/ui`, fitted to these tokens (no blur, no ring-as-border, unquoted `[class*=size-]` selectors so Tailwind
sees them).

### Buttons

`Button` (`ui/button.tsx`, variants in `ui/button-variants.ts`): `default` is the yellow primary, one per view at most;
`outline` for secondary actions on a page; `secondary` for a quieter filled action; `ghost` for toolbar and row actions;
`destructive` (solid red) only for the confirming button of a destructive dialog; `link` for inline navigation. Sizes:
`default` 32px, `lg` 36px for the main action of a page or form, `sm` 28px in rows. Labels name the action ("Enroll a
speaker", "Revoke token"), never "OK".

A button that runs a request (a form's submit, a row's Reset) is `aria-disabled` while it runs and ignores a second
press, never `disabled`: a disabled button under the focus drops it to the page in Chromium, so after a refusal the
focus would be nowhere. A refusal of a field moves the focus to that field; any other stays on the button beside its
message.

### Inputs

`Input` (`ui/input.tsx`) is 36px with the `--input` edge on the card, and takes the one focus outline from `base.css`
(no ring); `aria-invalid` turns its edge red, and the message below it says why (`role="alert"`, `text-destructive`,
`aria-describedby`). `Label` (`ui/label.tsx`) sits above its field. The login (`components/login/`) is the pattern: a
title and one line on the page, the form on a bordered card, one primary `lg` button.

`Textarea` and `NativeSelect` (the browser's own select) share the input's height, edge, surface and outline. `Switch`
is on in ink and off in the `--input` colour, never yellow (1.4:1 on a card). A required field marks its label with a
red `*` (read as "required"); a key without a title is an id, so its label is set in mono.

### Forms from schemas

`JsonForm` (`components/forms/json-form.tsx`) renders a JSON Schema's subset as fields in one column, 20px apart: a
nested object is a bordered fieldset named by its key, a fixed set of options a row of checkboxes, free list items
chips with a remove button and an Add field. A part the form cannot show is a mono textarea with "Edited as JSON: the
form cannot show this part." above it, so nothing is hidden or dropped. Errors show at their field after a submit is
tried, and the server's 422 lands on the field its `path` names (else beside the form, the path in mono). The submit
button sits below the fields, a Cancel ghost beside it when the form edits something in place. Inside a hand-made
form (a provider's configuration) the same fields sit in a fieldset named by what they configure, with that form's
submit. A list of short texts in a hand-made form (`TagListField`) is the renderer's chips and Add field: ids (tool
names) in mono, words (aliases) in the body face; an item it refuses stays in the field with its reason.

### Device pages

- The list is a `DataTable` (name, status, board, last seen), the enroll action as the page's one `lg` primary.
- The enroll wizard is a numbered stop list (`EnrollStep`): steps not reached yet are muted; the code is large mono with
  its countdown beside it, the command a mono block in a `figure` with a copy button (an installer board's with a
  muted line naming the readme that sets its variables); once the code expired unused, the last step says so without
  its spinner.
- A device's page: the name, its status pill and a ghost Rename (pencil) button, a muted line of board, last seen and id
  below them, then tabs: Overview, Health, Conversations and Inbox. Overview holds capabilities, settings and activity
  in the main column, the media and say cards in a 20rem side column (first on a phone), and the danger zone across the
  bottom.
  Each capability is one card titled by its id (an `h3`, its parts `h4`): state as a key-value list, the
  desired configuration with its `ApplyBadge` in the card's header (Device defaults while nothing was ever set),
  secrets as rows ("Set" or "Not set", never a value; the field masked, never a password field), commands as mono
  buttons opening their arguments and result below. Settings are a table per group; an overridden key's row wears the
  highlight wash.
- Forms and secret fields inside cards submit with the `secondary` button; the yellow stays the page's one action.
- Tabs (`ui/tabs.tsx`, Base UI) are words over a hairline, the active one in ink with a 2px ink underline, never yellow.
  A device's Overview stays mounted while hidden (an open editor keeps its draft); Health, Conversations and Inbox mount
  only while they show, so their reads start when they open and their polling stops when they close.
- Health: a Window select (Last 15 minutes, Last hour, Last 6 hours, Last 24 hours, the retention) and the device's round
  interval in a muted line, then a grid of cards (one column on a phone, two from md): CPU (the total, 0–100%),
  Memory (used, the axis reaching the limit; a limit of zero counts as none), Temperature, each a figure and a chart (see
  Charts), and Storage: per mountpoint a meter of its latest used share with its free space in words, and under it a
  small chart of the used share (1 − free / size) over the window; a mount that never reported its size shows its free
  space alone. Below them the top processes by CPU and by memory as two tables
  (process in mono, PID, CPU in percent of one core, memory), never a command line. A device without the
  `system.metrics` capability shows an empty state (telemetry is off on it) and reads nothing; a window without points
  says so on its card, and a cut read (50,000 points) says the server sent its first part.
- Rename puts the name's field in place of the title (36px, the whole name selected), Save the name (`secondary`) and
  Cancel (ghost); Enter saves, Escape cancels, and the page keeps an `sr-only` `h1` meanwhile. A refusal of the name
  (the client's own check, `name_taken`, `invalid`) sits on the field, any other beside the button.
- The danger zone is the last section (`h2` Danger zone): a bordered card saying what a delete removes, and an outline
  button in destructive text whose confirmation asks for the name typed (`typeToConfirm`). Once deleted, the page
  leaves for the list with the focus on the content and a success toast naming the device.
- Conversations is a `DataTable`: the time with its seconds, the trigger in words (Wake word, Button, Follow-up, Event;
  Not recorded muted), the mode in mono, the user's words and the reply cut to 120 characters as wrapping text, each
  tool as a mono `Tag` with its error code in destructive text, the tokens; an event turn's words carry a "Server's
  own" `Tag`. Columns hide by priority (reply below sm, tools below md, mode below lg, tokens below xl). Load older
  (outline) follows the table and hands the focus to the first older row.
- A turn opens in a drawer from the right (`Sheet`: the full width on a phone, 36rem from sm): its time as the title,
  trigger · mode · tokens below it, then its messages in order, each under its role in a small muted label (User,
  Assistant, Tool result; Server's own for an event turn's input). Speech is wrapping body text. The search, catalog,
  screen, video and music tools (`youtube_search`, `catalog_play`, `screen_state` and their kin) draw as cards: a call
  is its tool word, its query or ref and its filters as `Tag`s; a result is what it found or did — found rows over the
  server's own images (`/api/v1/catalog/images/{id}`, a muted box while there is none or it failed), a play's outcome, a
  screen's page and player — the stored JSON behind a "Raw JSON" disclosure on each, so the turn as the server stored
  it stays one click away. Every other tool keeps the raw figures: its call's arguments laid out as JSON and its result
  as mono preformatted blocks in figures named by their caption. Third-party text (titles, channels, replies) is always
  text, never markup; a result's error code sits in its card's header or its caption in destructive mono, as the list's
  tag sets it. Closing it gives the focus back to the row's Open.
- Focus: whatever opens in place takes it (an editor's first control, the secret field, the next wizard step's title);
  whatever closes gives it back to the button that opened it, or to the row's remaining button when that one went.
- The media card shows a placeholder square, never artwork from the source's hosts. The server serves the art itself
  (`get-device-artwork?h=`); showing it in the square is follow-up work.

### Memory, tasks and integrations

- Each page is sections (`h2` with a line below it), not tabs: Suggestions, Memories, Learning; a task's Question, Live
  view, Result, History, Notices; Home Assistant, MCP servers, Hermes, Subscriptions, Busy. Filters sit above their
  table as small labelled selects (a search field for Home Assistant's entities), and a long list grows with an outline
  "Show more" below it.
- Text that comes from a person, a model or another system (a memory, a task's question, result or transcript, a Home
  Assistant state, an MCP tool's description) is set as text that wraps (`whitespace-pre-wrap`, `break-words`), never
  as markup; a transcript is a mono block in a `figure`.
- A row's buttons name what they act on in their label (`Approve: <the memory's first words>`, `Edit access:
  <tool>`). A confirmation shows the thing it acts on (the memory, the question, the description the model will read).
- Focus: an action that takes its own row away (approve, reject, acknowledge) gives the focus to its section's heading
  (`tabIndex=-1`); one that ends the task's question or the task (reply, approve, deny, cancel) to the task's title.
- Subscriptions: a Speaker filter and Follow a topic (outline) above a `DataTable` (speaker, topic in mono, events as
  `Tag`s or "Every event", delivery, how long it lasts, who made it, when); Remove asks first. The create dialog orders
  Speaker, a Browse fieldset (a provider select, then what the module lists there: each entry's title as text with its
  topic in mono, Open for a subtree, Choose, Up, Choose everything here), Topic (mono, typed by hand too), Events
  (none chosen: every event), Delivery and Lasts as radios. A refusal by code lands on its field (`invalid_topic`,
  `unknown_module`, `unknown_instance` on the topic, `unsupported_event` on the events).
- Busy: the do-not-disturb control first (a duration select and Turn on do not disturb, `secondary`; once on, a warning
  pill with its end and End do not disturb), then the signals in force as a `DataTable` (source in words over its id in
  mono, reason, speakers, end; Home Assistant's ends "While the state holds" and has no Clear). The header carries the
  same state as a ghost menu button before the feed's tally: a bell (Not busy), or a struck bell and Busy or Do not
  disturb in warning text, naming in its menu each busy speaker with its reason and offering the durations and End do
  not disturb.
- A device's Inbox tab: Open questions first, each a card (when, kind, topic, the thread's title, the question as
  text), then Updates grouped by topic (the topic with the newest first), each update a row of its time, event `Tag`,
  text and state (Held while busy warning, Waiting to be told info, Unread neutral, Read success). A clarify question takes its
  options (radios, checkboxes when it allows several) or the owner's own words where it takes them, never both; an
  approval is a Decision radio group with Deny first and chosen, the send button naming the decision. An answer gives
  the focus to Open questions.
- A task's status is a `StatusBadge`: Waiting for an answer and Unknown in warning, Running in info, Failed in danger
  with its code, Done success, Queued and Cancelled neutral.

### Providers and modes

- A provider row's Test is a ghost button beside Delete; its outcome is a `StatusBadge` in the row's Test column, a
  polite live region: OK (success) with its milliseconds, a failure's words from the page's table (danger) with the
  server's code in mono, Cannot be tested neutral, Upstream rate limited warning, Testing (info) while it runs, and Wait
  with its seconds (neutral) while the server's limit holds; the button stays `aria-disabled` meanwhile, never
  disabled. Nothing upstream is ever shown.
- The create dialog orders name, module (grouped by kind), Configuration, Secrets; a module without configuration says
  so in muted text.
- The modes table sets names in mono, a missing model as muted "Device default", and the aliases a line per language
  behind the language's code in muted mono. The mode dialog is one column: name (create only), model, reasoning, the
  budgets as narrow fields (seconds, then the first token timeout in seconds beside it, and the token ceiling; the
  timeout and the ceiling are empty for their built-in defaults, the ceiling shown muted as "Default" in the table), interim text, then a Tools fieldset whose "All tools" switch (ink when on, never yellow)
  hides the list of names while on, then the voice aliases per language.

### Status

- `StatusBadge` (`data/status-badge.tsx`): glyph + words (+ an optional detail), `inline` in a cell or `pill` alone;
  `data-tone` carries the tone. Every state reads in colour, in shape and in words.
- `DeviceStatus`: Online, Offline, Updating, Enrolling.
- `ApplyBadge` (desired vs applied, from a capability's `desiredGen`, `appliedGen`, `appliedError`; `applyState` in
  `data/apply-state.ts`): Applied `gen 14`; Pending `gen 14 → 15` (the applied generation is behind the desired one);
  Failed with the desired generation struck and the device's code (`appliedError` on the generation it acknowledged).
- `SeverityLabel`: DEBUG, INFO, WARN, ERROR, fixed width so log lines align.
- `FeedStatus`: the live tally in the header; yellow "Live" while events stream, otherwise Connecting, Reconnecting in
  N s, Disconnected or Not connected; a polite `role="status"`.

### Tables

`DataTable` (`data/data-table.tsx`): columns declare `header`, `cell`, and optionally `sortValue` (a sortable header
button with `aria-sort`; empty values sort last both ways; `firstSort: 'desc'` for times), `align` and `className` (for
hiding by breakpoint). Row actions sit in a last, unlabeled-to-the-eye column ("Actions" for screen readers). `status`
is `ready`, `loading` (skeleton rows, `aria-busy`) or `error` (the `error` node across the table); an empty `rows`
shows the `empty` node across the table. The header is sticky inside the container: give the table a max height
(`className="max-h-[60svh]"`) and the container scrolls. **Virtualization:** that container is the scroll viewport and
rows have a fixed height (`--row-height`), so a long list swaps the body for a windowed one (TanStack Virtual) without
touching the columns.

### Timeline

`Timeline` (`data/timeline.tsx`), the signature: a route's stop list, newest on top. A bold time column, a line through
the stops, an icon per event kind inside each stop (turn, reply, tool, task, config, media, alarm, connection, error;
the kind is also read out), short text, and an expandable detail (native `details`, no script). With `live`, the newest
stop is yellow and flashes the highlight once as it arrives. Streams keep a bounded buffer (the feed client's job, at
most 500 entries).

### Logs

`LogView` / `LogLine` (`data/log-view.tsx`): monospace lines on one shared grid (subgrid), so time (with milliseconds),
severity, source (the speaker's name muted before the source chip, where lines of several share the view) and message
line up in columns; the source column is a fixed 10rem, so nothing shifts as lines scroll by. Attributes follow the
message as muted `key=value` (`truncated=true`, `continued=true`). On a phone the message takes its own line. The view
is its own scroll box (a fixed height, focusable) and windowed (TanStack Virtual, measured rows): only the lines in view
and a few around them are in the page. A mark where lines may be missing (the feed held some back past its 50 a second,
the tab fell behind, the feed reconnected) is a dashed rule across the grid in warning text, never dropped silently;
rows take padding, never margin, since the virtualizer measures each row's border box. While another filter's read
runs, the shown lines fade to 60% and the box is `aria-busy`. Read the newest, and turning Live off, read the newest
page alone again.

The Logs page (`/logs`): the page header with Read the newest (ghost) and the Live switch (ink when on), then one row
of filters (Speaker, Source in mono, Lowest level, Contains, From and Until in the household's zone) with a muted line
below naming the zone, and that a search by text or level reads the last day unless From is set. Stored lines come
newest first, 200 a page, Load older (outline) up to 2,000; past that a muted line asks for Until. A filter the server
would refuse is said on its field before anything is asked. Live swaps the stored lines for a tail of the lines stored
from then on, oldest first, following the end: a polite status says Following new lines; scrolling up pauses it
(Paused while you read), the view stops growing, a count of new lines and Jump to the latest lines (outline) appear,
and scrolling back to the end or the jump resumes it. The tail holds the feed's newest 2,000 lines, filtered in the
page, and is subscribed to the `logs` topic only while it shows.

### Charts

Small hand-written SVG charts (`devices/detail/health/metric-chart.tsx`), no chart library, one series each, so no
legend: the card's title names it. A plot of a fixed height whose SVG stretches to its width (strokes keep their 2px
through `vector-effect`), hairline solid gridlines in `--border` at round ticks, a 2px `--chart-1` line over a 10% wash
of it, broken where the device sent nothing for over three of its usual steps, and the latest point a 10px dot with a
2px card ring. Tick labels are HTML beside and below the plot, muted, tabular: values at round steps (a memory axis
reaches the limit), times on the household's clock (quarter hours, every 2 or 6 hours of its day). The latest value is
the card's figure (`text-2xl` semibold, proportional figures). The plot takes the keyboard (arrows, Home, End) and the
pointer: a hairline crosshair snaps to the nearest point and a small popover shows its value (strong) and time
(muted); the keyboard's readout is also said in a polite status. A Points disclosure under each chart lists its
points as a table, newest first, built only while open. Another window keeps the old chart at 60%, and its summary
names the window it drew, until its points land. About 240 points are drawn at most: the line is broken at the
device's gaps in the raw points first, then each run averaged down in proportion to its length, so a short gap still
shows on a day's window; where points were averaged the disclosure and the table say so (Points, averaged; Average).
A mount's chart under its meter is the same chart with a shorter plot.

### Cards, lists and states

- `Card` (`ui/card.tsx`): a bordered white surface with an `text-lg` title; never nested.
- `KeyValueList` (`data/key-value-list.tsx`): a `dl` grid; `mono` values wrap anywhere.
- `PageHeader`: title, an optional one-line description, actions on the right. No kicker above the title.
- `EmptyState` teaches what would be there and offers the one action that puts it there; `ErrorState` names the
  problem in codes only (`The server answered 503 unavailable.`, never a body or a value) and offers Try again;
  `LoadingLines` holds a block's shape while it loads.

### Overlays and feedback

- `ConfirmDialog` (`data/confirm-dialog.tsx`, Base UI alert dialog): for destructive or irreversible actions only. The
  confirm button shows a spinner while `onConfirm` runs, the dialog closes when it resolves, and stays open when it
  rejects (the caller has already shown why). Cancel is always there; Escape cancels unless the action is running. The
  confirm button is never disabled under the focus: while the action runs it is `aria-disabled` and ignores clicks, so
  the focus stays on it through a refusal.
- With `version` (a tool's hash, a question's token), `ConfirmDialog` answers what it showed when it opened: it keeps
  that description, hands `onConfirm` that version, and once the page reads a newer one under it says "Changed since
  you opened this" and blocks the confirm until it is opened again, so nothing unseen is approved.
- With `typeToConfirm`, `ConfirmDialog` opens on a field labelled "Type <name> to confirm" and runs nothing until the
  trimmed text is the name exactly: the confirm is `aria-disabled` meanwhile, and a press says so on the field and
  focuses it. The field starts empty at each opening.
- With `inlineError`, `ConfirmDialog` words a refusal inside itself, in codes (`The server answered 409 in_use.`), so the
  person who confirmed reads it where they can try again. Its `finalFocus` applies after a confirmed action only (a
  delete takes its own button away); a cancel gives the focus back to the trigger.
- `Dialog` (`ui/dialog.tsx`, shadcn's base-nova fitted like the alert dialog: the same scrim, border, shadow and 24px
  padding) holds a create form: a title and one line, the fields in one column 20px apart, then the footer with Cancel
  (outline) and the yellow submit. The form goes the moment the dialog starts closing (it is keyed by the opening), not
  after its fade, so what was typed into it (a secret too) is gone at once and a reopen in that window starts empty; it
  cannot be dismissed while its request runs. It opens on its first field and gives the focus back to the page's
  action.
- A value shown once (a new API token) replaces the form inside the same dialog: a warning-surface note that it will
  not be shown again, the value in a mono block in a `figure` (selectable whole), and a copy button that takes the
  focus. It lives in the dialog's state only and goes the moment the dialog starts closing. While it shows, only its Done
  button closes the dialog: a click on the scrim and Escape are refused, and the corner close button is hidden, so a
  stray press never throws it away.
- Toasts: `<Toaster>` is in the shell; `toast.add({ title, description, type })` (`ui/toast-manager.ts`) from an event
  handler, with `type` `success`, `info`, `warning`, `error` or `loading`. Toasts confirm what happened elsewhere or
  after a delay; an error the person must act on goes inline, not in a toast.
- Menus: `DropdownMenu` (theme menu, account menu, row actions).

### Navigation

`MainNav` lists the sections of S0 §13 in three groups (Overview, Devices; Providers, Modes, Memory, Tasks,
Integrations; Logs, Traces, Metrics, Settings, Tokens). A device's metrics are its page's Health tab, so Metrics stays out of
the nav until a household-wide page exists. Settings are the global ones (a Telemetry section holds the OTLP export and
log retention, each saying what the server does while unset: 7 days, off),
Tokens the API tokens: together they are S0's admin page, so there is no Admin section. **A section shows only once its
page exists:** `SECTION_ROUTES` in `shell/nav-sections.ts` is the feature list, and a task that adds a page adds its
route there, so the nav never holds a dead link. The active item is raised on the rail with its icon in yellow
(`aria-current="page"`). A section where something waits for the owner (Memory's suggestions) carries its count at the
row's end: a yellow pill, a mark the dark rail can carry, its number hidden from a screen reader, which hears the label
after the name instead ("Memory, 3 suggestions to review"); nothing shows at zero.

## Do's and Don'ts

### Do:

- Spend colour on what needs attention: warnings, failures, progress, the live tally, the one primary action.
- Word every state, give it its glyph, and let colour be the third signal.
- Set times bold and tabular, in the household's zone, down a column.
- Keep a failed or superseded change on screen, struck through with its code; nothing disappears silently.
- Say each fact once per view (one "Live", one status per object).
- Use the stop list for anything that happens over time and the table for anything compared side by side.
- Keep motion at 150–250 ms ease-out and only for state (hover, open, arrive); everything respects reduced motion.
- Regenerate the preview (`pnpm preview:build`) and keep `tokens.test.ts` green after any token change.

### Don't:

- Don't use the yellow for text, borders or charts on paper, or for a second accent.
- Don't add gradients, glass, blur or hero sections; don't put headline-number cards on the overview.
- Don't put a kicker or eyebrow label above a heading, or a coloured stripe on the side of a card.
- Don't nest cards, or give a bordered card a shadow too.
- Don't use monospace for prose or labels; it is for ids, codes and logs.
- Don't show a secret's value anywhere: secrets are `set` with a replace action.
- Don't link a nav section to a page that does not exist yet.
