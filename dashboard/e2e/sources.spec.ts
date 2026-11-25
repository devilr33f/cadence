import { expect, signIn, test } from './support'

// the dev stack runs no fake TorrServer or torrplayer (server/tools/fake* have no compose service yet), so the catalog
// is read as it stands: empty, with the ways to fill it, or the providers catalogSources names
test('shows the catalog\'s sources, or the ways to add one', async ({ page }) => {
  await signIn(page)
  await page.getByRole('navigation').getByRole('link', { name: 'Catalog' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Catalog' })).toBeVisible()
  const empty = page.getByText('No catalog sources yet')
  const listed = page.getByRole('main').getByRole('region').first()
  await expect(empty.or(listed).first()).toBeVisible()
  await expect(page.getByRole('link', { name: 'Manage the providers' }).first()).toHaveAttribute('href', '/providers')
  await expect(page.getByRole('link', { name: 'Choose the catalog sources' }).first()).toHaveAttribute('href', '/settings')
  if (await empty.isVisible())
    await expect(page.getByRole('search')).toHaveCount(0)
  else
    await expect(page.getByRole('search')).toBeVisible()
})
