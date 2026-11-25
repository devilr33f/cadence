import { useEffect, useRef } from 'react'

// a disclosure that hides its own trigger while open (an editor that replaces its Edit button) gives the focus back to
// it once it closes; `arm` before closing, and the trigger takes `ref`
export function useFocusReturn<T extends HTMLElement>(open: boolean) {
  const ref = useRef<T>(null)
  const armedRef = useRef(false)
  useEffect(() => {
    if (!open && armedRef.current) {
      armedRef.current = false
      ref.current?.focus()
    }
  }, [open])
  return {
    ref,
    arm: () => {
      armedRef.current = true
    },
  }
}
