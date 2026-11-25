import { expect, it } from 'vitest'
import { SUGGESTIONS_LIMIT, suggestionsBadge } from './memory'

it('words the suggestion count for the rail, a full read as at least that many', () => {
  expect(suggestionsBadge(0)).toEqual({ count: 0, label: 'no suggestions to review' })
  expect(suggestionsBadge(1)).toEqual({ count: 1, label: '1 suggestion to review' })
  expect(suggestionsBadge(3)).toEqual({ count: 3, label: '3 suggestions to review' })
  expect(suggestionsBadge(SUGGESTIONS_LIMIT)).toEqual({ count: 200, more: true, label: '200 or more suggestions to review' })
})
