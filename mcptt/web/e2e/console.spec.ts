// Live-server console flows: login → roster presence, broadcast call
// lifecycle, emergency alert with acknowledgement and archive, sign-out.
// Run via scripts/e2e.sh, which starts and seeds a real server.
import { expect, test } from '@playwright/test'

const GROUP = 'TAC-1'

async function login(page: import('@playwright/test').Page) {
  await page.goto('/')
  await page.getByLabel('Username').fill('dispatcher_1')
  await page.getByLabel('Password').fill('mcptt-demo-2026')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByText('Roster ·')).toBeVisible()
}

test('login lands on the live console with the seeded roster', async ({ page }) => {
  await login(page)
  await expect(page.getByText('LIVE', { exact: true })).toBeVisible()
  // Seeded field unit visible with its functional alias and priority tier.
  await expect(page.getByText('Bravo 2')).toBeVisible()
  await expect(page.getByText('TAC-1').first()).toBeVisible()
})

test('broadcast call starts and ends from the calls panel', async ({ page }) => {
  await login(page)
  await page.getByLabel('Broadcast to').selectOption({ label: GROUP })
  await page.getByRole('button', { name: '+ Broadcast' }).click()
  // The call card appears with kind and participant count.
  await expect(page.getByText('Broadcast ·').first()).toBeVisible()
  await page.getByRole('button', { name: 'End call' }).first().click()
  await expect(page.getByText('Broadcast ·')).toHaveCount(0)
})

test('emergency call raises an alert with location entry and acknowledge archives it', async ({ page }) => {
  await login(page)
  const tacRow = page.locator('li', { hasText: GROUP }).filter({ hasText: 'you are affiliated' }).first()
  await tacRow.getByRole('button', { name: 'Emergency', exact: true }).click()
  // Guard strip demands a deliberate confirm.
  await page.getByRole('button', { name: 'Confirm emergency' }).click()

  // Emergency state: banner chip, emergency call card in the calls panel.
  await expect(page.getByText('EMERGENCY ACTIVE')).toBeVisible()

  // The alert lands in the rail; acknowledge moves it to the collapsed
  // <details> archive — expand it and verify the acknowledgement landed.
  await page.getByRole('button', { name: 'Acknowledge' }).first().click()
  await page.getByText(/^Archive · \d+$/).click()
  const archived = page.getByLabel('Archived alerts')
  await expect(archived).toBeVisible()
  await expect(archived.getByText('Acknowledged by').first()).toBeVisible()
})

test('sign out returns to the login screen', async ({ page }) => {
  await login(page)
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page.getByLabel('Username')).toBeVisible()
})
