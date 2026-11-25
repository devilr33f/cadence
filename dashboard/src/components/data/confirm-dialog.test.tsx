// @vitest-environment jsdom
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { useRef } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/api/error'
import { Button } from '@/components/ui/button'
import { expectFocusOn } from '@/test/focus'
import { setupUser } from '@/test/typing'
import { ConfirmDialog } from './confirm-dialog'

afterEach(cleanup)

function renderDialog(onConfirm: () => Promise<void>) {
  render(
    <ConfirmDialog
      trigger={<Button variant="destructive">Revoke</Button>}
      title="Revoke this token?"
      description="Anything that uses it stops working at once."
      confirmLabel="Revoke token"
      tone="destructive"
      onConfirm={onConfirm}
    />,
  )
}

it('runs the action and closes once it is done', async () => {
  const user = setupUser()
  const onConfirm = vi.fn(async () => {})
  renderDialog(onConfirm)

  await user.click(screen.getByRole('button', { name: 'Revoke' }))
  const dialog = await screen.findByRole('alertdialog', { name: 'Revoke this token?' })
  await user.click(screen.getByRole('button', { name: 'Revoke token' }))

  expect(onConfirm).toHaveBeenCalledOnce()
  await waitFor(() => expect(dialog.isConnected).toBe(false))
})

it('stays open when the action fails, so it can be tried again', async () => {
  const user = setupUser()
  renderDialog(async () => {
    throw new Error('refused')
  })

  await user.click(screen.getByRole('button', { name: 'Revoke' }))
  await user.click(await screen.findByRole('button', { name: 'Revoke token' }))

  expect(await screen.findByRole('alertdialog', { name: 'Revoke this token?' })).toBeTruthy()
  expect((screen.getByRole('button', { name: 'Revoke token' }) as HTMLButtonElement).disabled).toBe(false)
})

it('cancels without running the action', async () => {
  const user = setupUser()
  const onConfirm = vi.fn(async () => {})
  renderDialog(onConfirm)

  await user.click(screen.getByRole('button', { name: 'Revoke' }))
  await user.click(await screen.findByRole('button', { name: 'Cancel' }))

  expect(onConfirm).not.toHaveBeenCalled()
})

it('words a refusal inside itself when asked to, and clears it on the next try', async () => {
  const user = setupUser()
  const onConfirm = vi.fn()
    .mockRejectedValueOnce(new ApiError(409, { error: 'in_use' }))
    .mockResolvedValueOnce(undefined)
  render(
    <ConfirmDialog
      trigger={<Button>Delete</Button>}
      title="Delete flux?"
      description="Settings that name it stop working."
      confirmLabel="Delete the provider"
      tone="destructive"
      inlineError
      onConfirm={onConfirm}
    />,
  )
  await user.click(screen.getByRole('button', { name: 'Delete' }))
  const dialog = await screen.findByRole('alertdialog', { name: 'Delete flux?' })
  await user.click(screen.getByRole('button', { name: 'Delete the provider' }))
  expect((await within(dialog).findByRole('alert')).textContent).toBe('The server answered 409 in_use.')
  await user.click(screen.getByRole('button', { name: 'Delete the provider' }))
  await waitFor(() => expect(dialog.isConnected).toBe(false))
})

it('gives the focus to finalFocus after a confirmed action, and back to its trigger after a cancel', async () => {
  const user = setupUser()
  function Page() {
    const afterRef = useRef<HTMLButtonElement>(null)
    return (
      <>
        <Button ref={afterRef}>Add</Button>
        <ConfirmDialog
          trigger={<Button>Delete</Button>}
          title="Delete flux?"
          description="Settings that name it stop working."
          confirmLabel="Delete the provider"
          finalFocus={afterRef}
          onConfirm={async () => {}}
        />
      </>
    )
  }
  render(<Page />)
  const trigger = screen.getByRole('button', { name: 'Delete' })
  await user.click(trigger)
  await user.click(await screen.findByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(document.activeElement).toBe(trigger))
  await user.click(trigger)
  await user.click(await screen.findByRole('button', { name: 'Delete the provider' }))
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add' })))
})

// what it acts on, versioned (a tool's hash, a question's token), as a page renders it from its latest data
function Versioned({ version, onConfirm }: { version: string, onConfirm: (opened?: string) => Promise<void> }) {
  return (
    <ConfirmDialog
      trigger={<Button>Acknowledge</Button>}
      title="Acknowledge echo?"
      description={`Description ${version}`}
      version={version}
      confirmLabel="Acknowledge the tool"
      onConfirm={onConfirm}
    />
  )
}

it('answers what it showed when it opened: a newer version is not sent, and blocks the confirm until it is opened again', async () => {
  const user = setupUser()
  const onConfirm = vi.fn(async (_opened?: string) => {})
  const { rerender } = render(<Versioned version="h2" onConfirm={onConfirm} />)
  await user.click(screen.getByRole('button', { name: 'Acknowledge' }))
  const dialog = await screen.findByRole('alertdialog', { name: 'Acknowledge echo?' })

  rerender(<Versioned version="h3" onConfirm={onConfirm} />)
  expect(within(dialog).getByText('Description h2')).toBeTruthy()
  expect(within(dialog).queryByText('Description h3')).toBeNull()
  expect(within(dialog).getByRole('status').textContent).toBe('Changed since you opened this: close it and open it again to see what it is now.')
  const confirm = within(dialog).getByRole('button', { name: 'Acknowledge the tool' })
  expect(confirm.getAttribute('aria-disabled')).toBe('true')
  await user.click(confirm)
  expect(onConfirm).not.toHaveBeenCalled()

  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(dialog.isConnected).toBe(false))
  await user.click(screen.getByRole('button', { name: 'Acknowledge' }))
  const again = await screen.findByRole('alertdialog', { name: 'Acknowledge echo?' })
  expect(within(again).getByText('Description h3')).toBeTruthy()
  expect(within(again).queryByRole('status')).toBeNull()
  await user.click(within(again).getByRole('button', { name: 'Acknowledge the tool' }))
  expect(onConfirm).toHaveBeenCalledWith('h3')
})

it('keeps the focus on its confirm button while the action runs and after a refusal', async () => {
  const user = setupUser()
  let refuse!: (error: Error) => void
  render(
    <ConfirmDialog
      trigger={<Button>Delete</Button>}
      title="Delete flux?"
      description="Settings that name it stop working."
      confirmLabel="Delete the provider"
      inlineError
      onConfirm={() => new Promise((_, reject) => {
        refuse = reject
      })}
    />,
  )
  await user.click(screen.getByRole('button', { name: 'Delete' }))
  const confirm = await screen.findByRole('button', { name: 'Delete the provider' })
  await user.click(confirm)
  expect(confirm.getAttribute('aria-disabled')).toBe('true')
  expect((confirm as HTMLButtonElement).disabled).toBe(false)
  refuse(new ApiError(409, { error: 'in_use' }))
  expect(await screen.findByRole('alert')).toBeTruthy()
  expect(document.activeElement).toBe(confirm)
})

describe('with a name to type', () => {
  function renderTyped(onConfirm: () => Promise<void>) {
    render(
      <ConfirmDialog
        trigger={<Button>Delete this speaker</Button>}
        title="Delete Kitchen?"
        description="It cannot be undone."
        confirmLabel="Delete the speaker"
        tone="destructive"
        typeToConfirm="Kitchen"
        inlineError
        onConfirm={onConfirm}
      />,
    )
  }

  it('opens on the field and runs nothing until the name is typed exactly', async () => {
    const user = setupUser()
    const onConfirm = vi.fn(async () => {})
    renderTyped(onConfirm)
    await user.click(screen.getByRole('button', { name: 'Delete this speaker' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Delete Kitchen?' })
    const field = within(dialog).getByRole('textbox', { name: 'Type Kitchen to confirm' })
    await expectFocusOn(field)
    const confirm = within(dialog).getByRole('button', { name: 'Delete the speaker' })
    expect(confirm.getAttribute('aria-disabled')).toBe('true')

    await user.click(confirm)
    expect(onConfirm).not.toHaveBeenCalled()
    expect(within(dialog).getByRole('alert').textContent).toBe('Type Kitchen exactly to confirm.')
    expect(field.getAttribute('aria-invalid')).toBe('true')
    await expectFocusOn(field)

    await user.type(field, 'KITCHEN')
    await user.click(confirm)
    expect(onConfirm).not.toHaveBeenCalled()

    await user.clear(field)
    await user.type(field, ' Kitchen ')
    expect(within(dialog).queryByRole('alert')).toBeNull()
    expect(confirm.getAttribute('aria-disabled')).toBeNull()
    await user.click(confirm)
    expect(onConfirm).toHaveBeenCalledOnce()
    await waitFor(() => expect(dialog.isConnected).toBe(false))
  })

  it('starts empty each time it opens', async () => {
    const user = setupUser()
    renderTyped(async () => {})
    await user.click(screen.getByRole('button', { name: 'Delete this speaker' }))
    let dialog = await screen.findByRole('alertdialog', { name: 'Delete Kitchen?' })
    await user.type(within(dialog).getByRole('textbox', { name: 'Type Kitchen to confirm' }), 'Kitch')
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(dialog.isConnected).toBe(false))
    await user.click(screen.getByRole('button', { name: 'Delete this speaker' }))
    dialog = await screen.findByRole('alertdialog', { name: 'Delete Kitchen?' })
    expect((within(dialog).getByRole('textbox', { name: 'Type Kitchen to confirm' }) as HTMLInputElement).value).toBe('')
  })
})
