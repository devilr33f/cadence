import { useEffect, useState } from 'react'

// a value once it held still for `ms`: a search typed key by key asks the server once it pauses
export function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const timer = setTimeout(setSettled, ms, value)
    return () => clearTimeout(timer)
  }, [value, ms])
  return settled
}
