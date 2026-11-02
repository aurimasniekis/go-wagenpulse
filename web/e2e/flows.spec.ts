import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'
import { credsFile, deliveryFile } from './env'
import { axe } from './axe'

// Interactive flows against the synthetic data created in global-setup.

test('vehicle overview decodes values; signal table, history chart and table', async ({ page }) => {
  await page.goto('/vehicles')
  await page.getByRole('link', { name: 'Test ID.4' }).click()
  await expect(page.getByRole('heading', { level: 1, name: /Test ID\.4/ })).toBeVisible()
  // The VIN is masked by default.
  await expect(page.getByText('•••••••••••000001')).toBeVisible()
  // Overview (default tab): labels instead of keys, decoded codes, converted units, overdue service.
  await expect(page.getByRole('tab', { name: 'Overview', selected: true })).toBeVisible()
  await expect(page.getByText('Outside temperature').first()).toBeVisible()
  await expect(page.getByText('15°C').first()).toBeVisible()
  await expect(page.getByText('All locked')).toBeVisible()
  await expect(page.getByText('12 d overdue').first()).toBeVisible()
  await axe(page)
  // All signals: label, raw key, decoded value.
  await page.getByRole('tab', { name: 'All signals' }).click()
  const state = page.getByTestId('state-table')
  await expect(state.getByText('High-voltage battery charge', { exact: true })).toBeVisible()
  await expect(state.getByText('hv_soc', { exact: true })).toBeVisible()
  await expect(state.getByText('Tailgate lock', { exact: true })).toBeVisible()
  await expect(state.getByRole('cell', { name: 'Locked', exact: true })).toBeVisible()
  await expect(state.getByText(/not in the dictionary \(observed key\)/)).toBeVisible()
  await axe(page)
  await state.getByText('High-voltage battery charge', { exact: true }).click()
  await expect(page.getByRole('tab', { name: 'History', selected: true })).toBeVisible()
  await expect(page.getByRole('img', { name: 'High-voltage battery charge history chart' })).toBeVisible()
  await page.locator('label', { hasText: /^Table$/ }).click()
  await expect(page.getByRole('cell', { name: '34%' }).first()).toBeVisible()
  await axe(page)
})

test('uploading the same delivery again is recognised as a duplicate', async ({ page }) => {
  await page.goto('/imports')
  // A different file name, identical content: duplicates are detected by SHA-256, never by name.
  const body = readFileSync(deliveryFile)
  await page
    .getByLabel('Delivery files to import')
    .setInputFiles({ name: 'renamed-copy.json', mimeType: 'application/json', buffer: body })
  await expect(page.getByText('renamed-copy.json')).toBeVisible()
  await page.getByRole('button', { name: /^Import 1 file/ }).click()
  await expect(page.getByRole('heading', { level: 1, name: /^Import/ })).toBeVisible()
  await expect(page.getByText('1 duplicate file(s)')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText('Identical content was imported before (matched by SHA-256).')).toBeVisible()
  await axe(page)
})

test('rule preview finds threshold crossings in stored history', async ({ page }) => {
  await page.goto('/rules')
  await page.getByRole('link', { name: 'Battery low (synthetic)' }).click()
  await page.getByRole('tab', { name: 'Preview' }).click()
  await page.getByRole('button', { name: 'Run preview' }).click()
  await expect(page.getByText(/observations evaluated/)).toBeVisible()
  await expect(page.getByRole('cell', { name: 'threshold.crossed' }).first()).toBeVisible()
  await axe(page)
})

test('API token is shown once and can be revoked', async ({ page }) => {
  const name = `e2e-${test.info().project.name}-${String(Date.now())}`
  await page.goto('/admin/tokens')
  await page.getByRole('button', { name: 'New token' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill(name)
  await dialog.getByRole('button', { name: 'read only' }).click()
  await dialog.getByRole('button', { name: 'Create token' }).click()
  await expect(dialog.getByTestId('secret-value')).toHaveText(/^wgp_at_/)
  await axe(page)
  await dialog.getByRole('button', { name: 'Done' }).click()
  const row = page.getByRole('row', { name: new RegExp(name) })
  await row.getByRole('button', { name: 'Revoke' }).click()
  await page.getByLabel(/Type .* to confirm/).fill(name)
  await page.getByRole('dialog').getByRole('button', { name: 'Revoke' }).click()
  await expect(row).toHaveCount(0)
})

test.describe('password sign-in', () => {
  // A fresh browser without the shared session: signing out here must not revoke the session other tests use.
  test.use({ storageState: { cookies: [], origins: [] } })

  test('sign in, then sign out', async ({ page }) => {
    const creds = JSON.parse(readFileSync(credsFile, 'utf8')) as { username: string; password: string }
    await page.goto('/vehicles')
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
    await axe(page)
    await page.getByLabel('Username').fill(creds.username)
    await page.getByLabel('Password').fill('wrong-password-123')
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page.getByRole('alert')).toBeVisible()
    await page.getByLabel('Password').fill(creds.password)
    await page.getByRole('button', { name: 'Sign in' }).click()
    // Returns to the page that required sign-in.
    await expect(page.getByRole('heading', { level: 1, name: 'Vehicles' })).toBeVisible()
    await page.getByRole('button', { name: /Account menu/ }).click()
    await page.getByRole('menuitem', { name: /Sign out/ }).click()
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  })
})

test('two-factor enrolment shows a scannable QR code and setup key', async ({ page }) => {
  await page.goto('/account')
  await page.getByRole('button', { name: 'Enable' }).click()
  await expect(page.getByRole('img', { name: 'QR code for your authenticator app' })).toBeVisible()
  await expect(page.getByTestId('totp-secret')).toHaveText(/^[A-Z2-7 ]{16,}$/)
  await axe(page)
  // Cancel without confirming: two-factor stays off, so the password sign-in test is unaffected.
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.getByRole('button', { name: 'Enable' })).toBeVisible()
})
