export type ApplyState = 'applied' | 'pending' | 'failed'

export interface Generations {
  desiredGen: number
  appliedGen: number
  appliedError: string
}

// a device acknowledges a generation with its error code: a newer desired generation is still on its way
export function applyState({ desiredGen, appliedGen, appliedError }: Generations): ApplyState {
  if (appliedGen < desiredGen)
    return 'pending'
  return appliedError ? 'failed' : 'applied'
}
