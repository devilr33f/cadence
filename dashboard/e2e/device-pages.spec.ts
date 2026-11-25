import type { Page } from '@playwright/test'
import { openSim, SIM, TRANSCRIPT } from './sim'
import { askForTurn, expect, holdsText, keptPastPage, test } from './support'

async function rename(page: Page, from: string, to: string) {
  await page.getByRole('button', { name: `Rename ${from}`, exact: true }).click()
  const field = page.getByRole('textbox', { name: 'Name', exact: true })
  await expect(field).toBeFocused()
  await field.fill(to)
  await page.getByRole('button', { name: 'Save the name' }).click()
  await expect(page.getByRole('heading', { level: 1, name: to, exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: `Rename ${to}`, exact: true })).toBeFocused()
}

// through the api from the page, with its session: the sim's name again
async function nameBack(page: Page, id: string) {
  await page.evaluate(async ({ id, name }) => {
    await fetch(`/api/v1/devices/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json', 'X-Cadence-CSRF': '1' },
      body: JSON.stringify({ name }),
    })
  }, { id, name: SIM })
}

test('renames the sim in its header, which the list follows, and names it back', async ({ page }) => {
  await openSim(page)
  const id = page.url().split('/devices/')[1]!
  const renamed = `${SIM}-renamed`
  let named = false
  try {
    await rename(page, SIM, renamed)
    await page.getByRole('navigation').getByRole('link', { name: 'Devices' }).click()
    await page.getByRole('link', { name: renamed, exact: true }).click()
    await rename(page, renamed, SIM)
    named = true
  }
  finally {
    // a failure between the renames would leave every later spec, and the ha client's smoke, without the sim's name
    if (!named)
      await nameBack(page, id)
  }
})

test('lists the scripted tool turn the sim takes in its conversations, with its tool and call', async ({ page }) => {
  await openSim(page)
  await page.getByRole('tab', { name: 'Conversations' }).click()
  const table = page.getByRole('tabpanel', { name: 'Conversations' }).getByRole('table', { name: `${SIM} conversations` })
  const newest = table.getByRole('row').nth(1)
  const open = newest.getByRole('button', { name: /^Open the turn of / })
  const before = await open.getAttribute('aria-label')
  askForTurn()
  // the turn's end refreshes the open tab: its row comes first, stamped with its own second
  await expect(open).not.toHaveAttribute('aria-label', before ?? '', { timeout: 45_000 })
  // the e2e's scripted model calls device_volume, then says nothing. booleans: the row shows the transcript, which a
  // failed matcher would print with its text
  const cells = {
    trigger: 'Button',
    user: TRANSCRIPT,
    // an empty reply's dash
    reply: '—',
    tool: 'device_volume',
  }
  for (const [cell, text] of Object.entries(cells))
    await expect.poll(() => holdsText(newest, text), { message: `the newest row's ${cell}` }).toBe(true)
  await open.click()
  const drawer = page.getByRole('dialog', { name: /^Turn of / })
  await expect(drawer.getByRole('figure', { name: 'Tool call device_volume' })).toContainText('"level": 10')
  await expect(drawer.getByRole('figure', { name: 'Result of device_volume' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(drawer).toBeHidden()
  await expect(open).toBeFocused()
  // held in memory only: neither the url nor the storage has it
  expect(await keptPastPage(page, TRANSCRIPT)).toBe(false)
})

test('shows the sim\'s cpu in its health tab, and its top processes by name, never a command line', async ({ page }) => {
  await openSim(page)
  await page.getByRole('tab', { name: 'Health' }).click()
  const panel = page.getByRole('tabpanel', { name: 'Health' })
  await expect(panel.getByRole('region', { name: 'CPU', exact: true })).toContainText(/\d+(?:\.\d+)?%\s*of all cores/, { timeout: 30_000 })
  const memory = panel.getByRole('table', { name: 'Top by memory' })
  await expect(memory).toContainText('cadenced')
  await expect(memory.getByRole('columnheader')).toHaveText(['Process', 'PID', 'CPU', 'Memory'])
})
