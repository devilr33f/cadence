import { act } from '@testing-library/react'
import { onTestFinished } from 'vitest'

function answer(animations: () => unknown[]) {
  Object.defineProperty(Element.prototype, 'getAnimations', {
    configurable: true,
    value: animations,
  })
}

// base ui keeps a closing popup mounted until its exit animation ends; jsdom runs none, so a test holds one running to
// see what the popup shows in that window, then lets it end
export function holdAnimations() {
  let finish!: () => void
  const finished = new Promise<void>((resolve) => {
    finish = resolve
  })
  const animation = {
    finished,
    pending: false,
    playState: 'running',
    effect: {
      getTiming: () => ({ duration: 150, iterations: 1 }),
    },
  }
  answer(() => [animation])
  // removed only once the test's components are unmounted (after its afterEach cleanup): a frame base ui asked for
  // while the animation ran may still read getAnimations after the release. a test that fails before its release must
  // not hold the next one's popups
  onTestFinished(() => {
    Reflect.deleteProperty(Element.prototype, 'getAnimations')
    finish()
  })
  return {
    async release() {
      answer(() => [])
      await act(async () => {
        finish()
        await finished
      })
    },
  }
}
