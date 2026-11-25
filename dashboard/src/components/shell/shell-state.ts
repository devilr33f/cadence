// what the shell shows about the live feed and the session; the feed client and the session guard fill these in
export type FeedState
  = | { state: 'idle' }
    | { state: 'connecting' }
    | { state: 'live' }
    | { state: 'reconnecting', inSeconds?: number }
    | { state: 'closed' }

export type AccountState
  = | { state: 'signed-out' }
    | { state: 'signed-in', onSignOut?: () => void }
