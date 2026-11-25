import { expect, signIn, test } from './support'

// a fake module of the server's catalog: a speech instance no setting names, so it can go again
const NAME = 'dashboard-e2e-stt'

test('adds a provider from the module catalog, tests it ok, and deletes it', async ({ page }) => {
  await signIn(page)
  await page.getByRole('navigation').getByRole('link', { name: 'Providers' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Providers' })).toBeVisible()
  const add = page.getByRole('button', { name: 'Add a provider' }).first()
  await add.click()

  const dialog = page.getByRole('dialog', { name: 'Add a provider' })
  await dialog.getByRole('textbox', { name: 'Name (required)' }).fill(NAME)
  const module = dialog.getByRole('combobox', { name: 'Module (required)' })
  // the catalog groups its modules by kind
  await expect(module.locator('optgroup[label="Speech to text"] option', { hasText: 'fake-stt' })).toHaveCount(1)
  await module.selectOption('fake-stt')
  // the config is the module's own schema, rendered as a form: optional fields, left to the module's defaults
  const config = dialog.getByRole('group', { name: 'Configuration' })
  await expect(config.getByRole('textbox', { name: 'finalAfterPackets', exact: true })).toBeVisible()
  await expect(config.getByRole('textbox', { name: 'text', exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: 'Add the provider' }).click()
  await expect(dialog).toBeHidden()
  await expect(add).toBeFocused()

  const row = page.getByRole('row').filter({
    has: page.getByText(NAME, { exact: true }),
  })
  await expect(row).toContainText('fake-stt')
  await expect(row).toContainText('Speech to text')
  await row.getByRole('button', { name: `Test ${NAME}` }).click()
  await expect(row.getByRole('status')).toContainText(/OK\s*\d+ ms/)

  await row.getByRole('button', { name: `Delete ${NAME}` }).click()
  await page.getByRole('alertdialog', { name: `Delete ${NAME}?` }).getByRole('button', { name: 'Delete the provider' }).click()
  await expect(row).toHaveCount(0)
  await expect(add).toBeFocused()
})
