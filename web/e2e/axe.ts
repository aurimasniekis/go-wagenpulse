import AxeBuilder from '@axe-core/playwright'
import { expect, type Page } from '@playwright/test'

/** Fails on any WCAG 2.1 A/AA violation. Charts are labelled images whose data is also offered as a table. */
export async function axe(page: Page) {
  const res = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  const summary = res.violations.map(
    (v) =>
      `${v.id} (${v.impact ?? ''}): ${v.nodes
        .map((n) => n.target.join(' '))
        .slice(0, 3)
        .join(' | ')} — ${v.nodes[0]?.any[0]?.message ?? ''}`,
  )
  expect(summary).toEqual([])
}
