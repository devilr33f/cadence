import type { TurnTokens, TurnView } from '@/api/gen'
import { stampText } from '@/lib/format-time'

type Trigger = NonNullable<TurnView['trigger']>

// the names the conversation history stores, as the live feed's turn_start carries them
const TRIGGERS: Readonly<Record<Trigger, string>> = {
  wake: 'Wake word',
  button: 'Button',
  follow_up: 'Follow-up',
  event: 'Event',
  // a direct command a wake-word spotter heard offline
  word: 'Command word',
  // an update the server spoke unasked: one assistant message, no user message
  notice: 'Notice',
  // a card action a screen's remote ran, without the model
  screen: 'Screen',
  // a deterministic reset turn: stored, but never read into the next prompt
  reset: 'Reset',
  // a request the owner typed in the dashboard's assistant
  dashboard: 'Dashboard',
}

// a turn stored before turns recorded what started it
export const UNRECORDED = 'Not recorded'

// an event or screen turn's input is the server's own framing (a task's update to tell, a card action a remote ran),
// never anybody's speech
export const SERVER_OWN = 'Server\'s own'

// a trigger in words; one this page does not know is shown as its token
export function triggerText(trigger: string | undefined): string | undefined {
  return trigger !== undefined && Object.hasOwn(TRIGGERS, trigger)
    ? TRIGGERS[trigger as Trigger]
    : undefined
}

export function isEventTurn(turn: Pick<TurnView, 'trigger'>): boolean {
  return turn.trigger === 'event' || turn.trigger === 'screen'
}

// a row shows the start of what was said and replied; the drawer shows it whole
const CUT_AT = 120

export function cut(text: string): string {
  const chars = [...text]
  return chars.length > CUT_AT
    ? `${chars.slice(0, CUT_AT).join('').trimEnd()}…`
    : text
}

const COUNT = new Intl.NumberFormat('en-GB')

export function tokensText(tokens: TurnTokens): string {
  return `${COUNT.format(tokens.prompt)} in · ${COUNT.format(tokens.completion)} out`
}

// a turn's moment: turns of one conversation come seconds apart
export function turnStamp(turn: Pick<TurnView, 'at'>, timeZone: string | null): string {
  return stampText(turn.at, timeZone, true)
}

// what started the turn, the mode it ran in and what it cost, for the drawer's line under its title
export function turnSummary(turn: TurnView): string {
  return [
    triggerText(turn.trigger) ?? turn.trigger ?? UNRECORDED,
    turn.mode,
    turn.tokens && tokensText(turn.tokens),
  ].filter(Boolean).join(' · ')
}
