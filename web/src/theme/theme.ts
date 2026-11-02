import { createTheme, type CSSVariablesResolver, type MantineColorsTuple } from '@mantine/core'
import { cssVars, dark, light, radius } from './tokens'

// Ten-shade scales for Mantine components (index 6 = filled colour in light mode, 8 = dark mode default).
const cobalt: MantineColorsTuple = [
  '#e6f2ff',
  '#cce3ff',
  '#99c6ff',
  '#66a9ff',
  '#3d93ff',
  '#1f8fff',
  '#0b6fd8',
  '#0b5bb5',
  '#0a4e9b',
  '#073a73',
]
const amber: MantineColorsTuple = [
  '#fff9e0',
  '#fff0b3',
  '#ffe680',
  '#ffdb4d',
  '#ffd11a',
  '#ffc600',
  '#e0ad00',
  '#c98d00',
  '#a87500',
  '#7a5500',
]

export const theme = createTheme({
  primaryColor: 'cobalt',
  // Filled components pick black or white text by luminance. 0.179 is where both reach ~4.58:1, so every filled
  // colour (any shade) gets AA text; Mantine's default (white on most fills) fails on bright blues, reds and oranges.
  autoContrast: true,
  luminanceThreshold: 0.179,
  // One shade for both schemes: Mantine's autoContrast decides text colour from the light shade in JS, so a different
  // dark shade would get the wrong (failing) text colour.
  primaryShade: { light: 7, dark: 7 },
  colors: { cobalt, amber },
  fontFamily: "'Inter Variable', Inter, system-ui, -apple-system, 'Segoe UI', sans-serif",
  fontFamilyMonospace: "'JetBrains Mono Variable', 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, monospace",
  headings: {
    fontFamily: "'Inter Variable', Inter, system-ui, sans-serif",
    fontWeight: '650',
    sizes: {
      h1: { fontSize: '1.625rem', lineHeight: '1.25' },
      h2: { fontSize: '1.25rem', lineHeight: '1.3' },
      h3: { fontSize: '1.0625rem', lineHeight: '1.35' },
      h4: { fontSize: '0.9375rem', lineHeight: '1.4' },
    },
  },
  radius,
  defaultRadius: 'sm',
  focusRing: 'auto',
  cursorType: 'pointer',
  fontSizes: { xs: '0.75rem', sm: '0.8125rem', md: '0.875rem', lg: '1rem', xl: '1.125rem' },
  components: {
    Button: { defaultProps: { radius: 'sm' } },
    Badge: { defaultProps: { radius: 'xs', variant: 'light' } },
    Card: { defaultProps: { radius: 'md', withBorder: true, padding: 'lg' } },
    Paper: { defaultProps: { radius: 'md' } },
    Modal: {
      defaultProps: {
        radius: 'md',
        centered: true,
        overlayProps: { backgroundOpacity: 0.55, blur: 2 },
        closeButtonProps: { 'aria-label': 'Close dialog' },
      },
    },
    Drawer: { defaultProps: { closeButtonProps: { 'aria-label': 'Close panel' } } },
    Notification: { defaultProps: { closeButtonProps: { 'aria-label': 'Dismiss notification' } } },
    Alert: { defaultProps: { closeButtonLabel: 'Dismiss' } },
    Tooltip: { defaultProps: { withArrow: true, openDelay: 250 } },
    TextInput: { defaultProps: { radius: 'sm' } },
    Select: { defaultProps: { radius: 'sm', allowDeselect: false } },
    Table: { defaultProps: { verticalSpacing: 'xs', horizontalSpacing: 'sm' } },
  },
})

/** Exposes the product tokens as --wp-* variables and maps Mantine's body/background variables onto them. */
export const cssVariablesResolver: CSSVariablesResolver = () => ({
  variables: {},
  light: {
    ...cssVars(light),
    '--mantine-color-body': light.bg,
    '--mantine-color-text': light.text,
    '--mantine-color-dimmed': light.textMuted,
    '--mantine-color-default-border': light.border,
    '--mantine-color-anchor': light.link,
  },
  dark: {
    ...cssVars(dark),
    '--mantine-color-body': dark.bg,
    '--mantine-color-text': dark.text,
    '--mantine-color-dimmed': dark.textMuted,
    '--mantine-color-default': dark.surface2,
    '--mantine-color-default-hover': dark.surface3,
    '--mantine-color-default-border': dark.border,
    '--mantine-color-anchor': dark.link,
    '--mantine-color-dark-7': dark.surface,
    '--mantine-color-dark-6': dark.surface2,
    '--mantine-color-dark-5': dark.surface3,
    '--mantine-color-dark-4': dark.border,
  },
})
