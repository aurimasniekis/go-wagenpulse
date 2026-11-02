import { badgeColors, dark, light, type Palette } from './tokens'

function channel(c: number): number {
  const s = c / 255
  return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
}

function luminance(hex: string): number {
  const v = hex.replace('#', '')
  const r = parseInt(v.slice(0, 2), 16)
  const g = parseInt(v.slice(2, 4), 16)
  const b = parseInt(v.slice(4, 6), 16)
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

export function contrast(a: string, b: string): number {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p) as [number, number]
  return (x + 0.05) / (y + 0.05)
}

const pairs = (p: Palette): [string, string, string, number][] => [
  ['text on bg', p.text, p.bg, 7],
  ['text on surface', p.text, p.surface, 7],
  ['muted text on surface', p.textMuted, p.surface, 4.5],
  ['muted text on surface2', p.textMuted, p.surface2, 4.5],
  ['dim text on surface', p.textDim, p.surface, 4.5],
  ['primary button text', p.onPrimary, p.primary, 4.5],
  ['accent button text', p.onAccent, p.accent, 4.5],
  ['link on surface', p.link, p.surface, 4.5],
  ['success text on surface', p.success, p.surface, 4.5],
  ['warning text on surface', p.warning, p.surface, 4.5],
  ['danger text on surface', p.danger, p.surface, 4.5],
  ['info text on surface', p.info, p.surface, 4.5],
  ['accent text on bg', p.accentText, p.bg, 4.5],
  ['accent text on surface2', p.accentText, p.surface2, 4.5],
  ['focus ring on bg (non-text)', p.focus, p.bg, 3],
  // Light-variant badges sit on a faint tint of their colour; surface2/surface3 bound that from below.
  ...badgeColors.flatMap((c): [string, string, string, number][] => [
    [`${c} badge text on surface`, p.badgeText[c], p.surface, 4.5],
    [`${c} badge text on surface2`, p.badgeText[c], p.surface2, 4.5],
    [`${c} badge text on surface3`, p.badgeText[c], p.surface3, 4.5],
  ]),
]

describe.each([
  ['light', light],
  ['dark', dark],
])('%s theme meets WCAG AA', (_name, palette) => {
  it.each(pairs(palette))('%s', (_label, fg, bg, min) => {
    expect(contrast(fg, bg)).toBeGreaterThanOrEqual(min)
  })
})
