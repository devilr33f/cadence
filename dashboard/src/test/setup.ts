import { configure } from '@testing-library/react'
import { afterEach, beforeEach } from 'vitest'
import { guardConsoleErrors } from './console-guard'

// headroom for a loaded gate (make ci runs this suite beside the go and c++ builds): a findBy or waitFor that the idle
// suite settles in milliseconds may need seconds there. the cost of a slow test is fixed at its root (setupUser, enter),
// never by more waiting
configure({ asyncUtilTimeout: 3000 })

let check: (() => void) | undefined

beforeEach(() => {
  check = guardConsoleErrors()
})

afterEach(() => {
  const done = check
  check = undefined
  done?.()
})
