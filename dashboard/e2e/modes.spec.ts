import type { Locator, Page } from '@playwright/test'
import { expect, signIn, test } from './support'

const MODE = 'dashboard-e2e'

async function openModes(page: Page) {
  await page.getByRole('navigation').getByRole('link', { name: 'Modes' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Modes' })).toBeVisible()
}

function modeRow(page: Page): Locator {
  return page.getByRole('row').filter({
    has: page.getByText(MODE, { exact: true }),
  })
}

// the global allowedModes row, reached by the modes page's link, which hands the focus to the row's own button
async function openAllowedModes(page: Page): Promise<Locator> {
  await page.getByRole('link', { name: 'Open the allowedModes setting' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Settings' })).toBeVisible()
  const row = page.getByRole('row', { name: 'Allowed modes', exact: true })
  await expect(row.getByRole('button', { name: /^(?:Set|Edit) Allowed modes$/ })).toBeFocused()
  return row
}

async function deleteMode(page: Page): Promise<Locator> {
  await modeRow(page).getByRole('button', { name: `Delete ${MODE}` }).click()
  const confirm = page.getByRole('alertdialog', { name: `Delete ${MODE}?` })
  await confirm.getByRole('button', { name: 'Delete the mode' }).click()
  return confirm
}

test('adds a mode, which the server keeps while allowedModes names it, then deletes it', async ({ page }) => {
  await signIn(page)
  await openModes(page)
  const add = page.getByRole('button', { name: 'Add a mode' }).first()
  await add.click()
  const dialog = page.getByRole('dialog', { name: 'Add a mode' })
  await dialog.getByRole('textbox', { name: 'Name (required)' }).fill(MODE)
  await dialog.getByRole('button', { name: 'Add the mode' }).click()
  await expect(dialog).toBeHidden()
  await expect(add).toBeFocused()
  // a new mode takes fast's budgets and every tool
  await expect(modeRow(page)).toContainText('All')

  let setting = await openAllowedModes(page)
  const start = setting.getByRole('button', { name: /^(?:Set|Edit) Allowed modes$/ })
  // unset, the list is the server's default (fast and smart), which the global mode must stay in
  const wasSet = (await start.textContent())?.trim() === 'Edit'
  await start.click()
  const editor = page.getByRole('form', { name: /^(?:Set|Edit) Allowed modes$/ })
  const item = editor.getByRole('textbox', { name: /^Allowed modes/ })
  for (const name of [
    ...(wasSet
      ? []
      : ['fast', 'smart']),
    MODE,
  ]) {
    await item.fill(name)
    await item.press('Enter')
  }
  await editor.getByRole('button', { name: 'Save the setting' }).click()
  await expect(editor).toBeHidden()
  await expect(setting).toContainText(MODE)

  await openModes(page)
  const refused = await deleteMode(page)
  await expect(refused.getByRole('alert')).toHaveText('The server answered 409 in_use.')
  await refused.getByRole('button', { name: 'Cancel' }).click()
  await expect(refused).toBeHidden()
  await expect(modeRow(page)).toHaveCount(1)

  setting = await openAllowedModes(page)
  if (wasSet) {
    await setting.getByRole('button', { name: 'Edit Allowed modes' }).click()
    await editor.getByRole('button', { name: `Remove ${MODE}` }).click()
    await editor.getByRole('button', { name: 'Save the setting' }).click()
    await expect(editor).toBeHidden()
  }
  else {
    await setting.getByRole('button', { name: 'Reset Allowed modes' }).click()
    await expect(setting.getByRole('button', { name: 'Set Allowed modes' })).toBeFocused()
  }
  await expect(setting).not.toContainText(MODE)

  await openModes(page)
  const confirm = await deleteMode(page)
  await expect(confirm).toBeHidden()
  await expect(modeRow(page)).toHaveCount(0)
  await expect(add).toBeFocused()
})
