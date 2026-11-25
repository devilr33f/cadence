// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setupUser } from '@/test/typing'
import { SecretFileLoader } from './secret-file'
import { fileTextOf } from './secret-file-data'

afterEach(cleanup)

const COOKIES = '# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\tvalue\n'

describe('the secret file loader', () => {
  it('puts the file\'s text in the field, and nowhere else', async () => {
    const user = setupUser()
    const field = document.createElement('input')
    const onLoaded = vi.fn()
    render(<SecretFileLoader id="cookies" target={() => field} onLoaded={onLoaded} />)
    const picker = document.getElementById('cookies-file') as HTMLInputElement
    await user.upload(picker, new File([COOKIES], 'cookies.txt', { type: 'text/plain' }))
    await screen.findByText('Loaded from a file.')
    expect(field.value).toBe('')
    expect(fileTextOf(field)).toBe(COOKIES)
    expect(onLoaded).toHaveBeenCalledOnce()
    expect(picker.value).toBe('')
    expect(document.body.innerHTML).not.toContain(COOKIES)
  })

  it('refuses a file over the server\'s bound', async () => {
    const user = setupUser()
    const field = document.createElement('input')
    render(<SecretFileLoader id="cookies" target={() => field} onLoaded={() => {}} />)
    const picker = document.getElementById('cookies-file') as HTMLInputElement
    await user.upload(picker, new File([
      new Uint8Array(512 * 1024 + 1),
    ], 'big.txt', { type: 'text/plain' }))
    await screen.findByText('That file is over 512 KiB.')
    expect(field.value).toBe('')
  })
})
