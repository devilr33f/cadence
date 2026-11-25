// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { holdAnimations } from './animations'

function nextFrame(): Promise<void> {
  return new Promise(resolve => requestAnimationFrame(() => resolve()))
}

// base ui reads getAnimations once when a popup starts closing or opening, then again on the next animation frame: a
// frame it asked for while the animations were held may run after the release (the unit-test flake of task 5b)
it('answers no animations, never none at all, to a frame that runs after the release', async () => {
  const fade = holdAnimations()
  let seen: unknown
  requestAnimationFrame(() => {
    seen = document.body.getAnimations()
  })
  await fade.release()
  await nextFrame()
  expect(seen).toEqual([])
})
