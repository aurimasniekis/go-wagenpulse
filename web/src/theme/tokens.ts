/**
 * WagenPulse design tokens. The palette is inspired by Cobalt2 (deep navy surfaces, electric blue, yellow/orange
 * accents) and adapted for accessible light and dark product themes: every text/background pairing below is checked
 * against WCAG AA in contrast.test.ts.
 */

export interface Palette {
  /** Page background. */
  bg: string
  /** Primary surface (cards, tables). */
  surface: string
  /** Raised/secondary surface (sidebar, table headers, inputs). */
  surface2: string
  /** Hover/selected surface. */
  surface3: string
  border: string
  borderStrong: string
  text: string
  textMuted: string
  textDim: string
  /** Primary action colour (buttons, links) and the text on top of it. */
  primary: string
  primaryHover: string
  onPrimary: string
  /** Signature accent (Cobalt2 yellow) and text on top of it. */
  accent: string
  onAccent: string
  /** The accent used as text (the wordmark): yellow on navy, deep amber on paper. */
  accentText: string
  link: string
  success: string
  warning: string
  danger: string
  info: string
  /** Background tints for semantic badges. */
  successSoft: string
  warningSoft: string
  dangerSoft: string
  infoSoft: string
  focus: string
  /** Chart series colours, in order. */
  series: string[]
  chartGrid: string
  chartAxis: string
  /** Text of light-variant badges and alerts per Mantine colour (Mantine's defaults fail AA on light surfaces). */
  badgeText: Record<BadgeColor, string>
}

/** Mantine colours used for badges and alerts; each gets an AA-checked text colour per theme. */
export const badgeColors = [
  'green',
  'blue',
  'cobalt',
  'yellow',
  'orange',
  'red',
  'gray',
  'grape',
  'cyan',
  'amber',
] as const
export type BadgeColor = (typeof badgeColors)[number]

export const dark: Palette = {
  bg: '#0e1d2a',
  surface: '#132a3c',
  surface2: '#193549',
  surface3: '#21445e',
  border: '#24465f',
  borderStrong: '#335b7a',
  text: '#e8eef5',
  textMuted: '#a9bdd0',
  textDim: '#8aa2b8',
  primary: '#0b5bb5',
  primaryHover: '#0b6fd8',
  onPrimary: '#ffffff',
  accent: '#ffc600',
  onAccent: '#122738',
  accentText: '#ffc600',
  link: '#6fbaff',
  success: '#5ee05a',
  warning: '#ffb340',
  danger: '#ff7a9c',
  info: '#7fe7f2',
  successSoft: 'rgba(94, 224, 90, 0.14)',
  warningSoft: 'rgba(255, 179, 64, 0.14)',
  dangerSoft: 'rgba(255, 122, 156, 0.14)',
  infoSoft: 'rgba(127, 231, 242, 0.12)',
  focus: '#ffc600',
  series: ['#1f8fff', '#ffc600', '#2ec4d6', '#ff7a9c', '#5ee05a', '#b79cff', '#ff9d00', '#a9bdd0'],
  chartGrid: '#21445e',
  chartAxis: '#8aa2b8',
  badgeText: {
    green: '#5ee05a',
    blue: '#6fbaff',
    cobalt: '#6fbaff',
    yellow: '#ffc94d',
    orange: '#ffb340',
    red: '#ff8fab',
    gray: '#c3d3e2',
    grape: '#cdb4ff',
    cyan: '#7fe7f2',
    amber: '#ffc600',
  },
}

export const light: Palette = {
  bg: '#f3f6f9',
  surface: '#ffffff',
  surface2: '#eaf0f6',
  surface3: '#dbe6f1',
  border: '#d3dde8',
  borderStrong: '#a9b9ca',
  text: '#0f2233',
  textMuted: '#40566b',
  textDim: '#5b6f82',
  primary: '#0b5bb5',
  primaryHover: '#0a4e9b',
  onPrimary: '#ffffff',
  accent: '#ffc600',
  onAccent: '#122738',
  accentText: '#8a5a00',
  link: '#0b5bb5',
  success: '#1c7413',
  warning: '#995400',
  danger: '#b8164f',
  info: '#0b6a78',
  successSoft: 'rgba(28, 116, 19, 0.10)',
  warningSoft: 'rgba(153, 84, 0, 0.10)',
  dangerSoft: 'rgba(184, 22, 79, 0.09)',
  infoSoft: 'rgba(11, 106, 120, 0.09)',
  focus: '#0b5bb5',
  series: ['#0b5bb5', '#c98d00', '#0b8a9a', '#c2185b', '#2f8a24', '#6f4bd8', '#d96c00', '#5b6f82'],
  chartGrid: '#e3eaf2',
  chartAxis: '#5b6f82',
  badgeText: {
    green: '#1c7413',
    blue: '#0b5bb5',
    cobalt: '#0b5bb5',
    yellow: '#7a4f00',
    orange: '#9a3d00',
    red: '#b8164f',
    gray: '#40566b',
    grape: '#6b2d8f',
    cyan: '#0b6a78',
    amber: '#8a5a00',
  },
}

/** Spacing scale (px) shared by layouts. */
export const space = { xs: 4, sm: 8, md: 12, lg: 16, xl: 24, xxl: 32 } as const

/** Restrained radii: square-ish, never pill-heavy. */
export const radius = { xs: '2px', sm: '4px', md: '6px', lg: '8px', xl: '10px' } as const

/** Exposes a palette as CSS custom properties (--wp-*). */
export function cssVars(p: Palette): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(p)) {
    if (Array.isArray(v)) {
      v.forEach((c: string, i) => (out[`--wp-series-${String(i)}`] = c))
    } else if (typeof v === 'object') {
      // badgeText: applied to Mantine's own variables by the theme's cssVariablesResolver.
      for (const [c, hex] of Object.entries(v as Record<string, string>)) {
        out[`--mantine-color-${c}-light-color`] = hex
        out[`--mantine-color-${c}-outline`] = hex
      }
    } else {
      out[`--wp-${k.replace(/[A-Z]/g, (m) => `-${m.toLowerCase()}`)}`] = v as string
    }
  }
  return out
}
