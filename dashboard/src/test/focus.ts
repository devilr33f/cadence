import { waitFor } from '@testing-library/react'
import { expect } from 'vitest'

// a focus that an answer or a dialog moves lands a moment after the page shows it: react runs the effect that moves it
// after the commit, in a task of its own, which a loaded machine runs after the test's next line (user-event's wait
// is one timer). wait for it, then check that nothing moves it on
export async function expectFocusOn(element: Element) {
  await waitFor(() => expect(document.activeElement).toBe(element))
  await new Promise(resolve => setTimeout(resolve, 20))
  expect(document.activeElement).toBe(element)
}
