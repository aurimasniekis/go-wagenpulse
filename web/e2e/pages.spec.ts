import { expect, test, type Page } from '@playwright/test'
import { axe } from './axe'

// Every page renders without console errors or failed requests and passes axe (WCAG 2.1 AA) in light and dark.
const pages: [string, string][] = [
  ['/', 'Fleet'],
  ['/vehicles', 'Vehicles'],
  ['/explorer', 'Data explorer'],
  ['/signals', 'Signal dictionary'],
  ['/imports', 'Imports'],
  ['/imports?tab=deliveries', 'Imports'],
  ['/sources', 'Sources'],
  ['/rules', 'Rules'],
  ['/rules/new', 'New rule'],
  ['/events', 'Events & deliveries'],
  ['/events?tab=deliveries', 'Events & deliveries'],
  ['/adapters', 'Adapters'],
  ['/routing', 'Routing'],
  ['/admin/users', 'Users'],
  ['/admin/tokens', 'API tokens'],
  ['/admin/sso', 'Single sign-on'],
  ['/admin/settings', 'Settings'],
  ['/admin/audit', 'Audit log'],
  ['/admin/system', 'System'],
  ['/account', 'Account & security'],
]

function watch(page: Page) {
  const problems: string[] = []
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`))
  page.on('console', (m) => {
    if (m.type() === 'error') problems.push(`console: ${m.text()}`)
  })
  page.on('response', (r) => {
    if (r.url().includes('/api/') && r.status() >= 400) problems.push(`${String(r.status())} ${r.url()}`)
  })
  return problems
}

for (const [path, title] of pages) {
  test(`${path} renders and is accessible`, async ({ page }) => {
    const problems = watch(page)
    await page.goto(path)
    // Titles may carry a badge (e.g. Sources: “unofficial portal interface”), so match the title as a prefix.
    await expect(
      page.getByRole('heading', { level: 1, name: new RegExp(`^${title.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`) }),
    ).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expect(page.getByText('Request failed')).toHaveCount(0)
    await axe(page)
    expect(problems).toEqual([])
  })
}
