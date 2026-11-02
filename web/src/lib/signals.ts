import type { Signal } from '@/api/client'
import { fmtNumber, fmtValue } from './format'

/**
 * Presentation of signal values. Stored observations are never changed; these helpers only decide how a value is
 * shown: the dictionary-derived label, documented enum codes decoded to words, documented units converted to familiar
 * ones (deci-Kelvin → °C), and service intervals that have passed shown as overdue.
 */

type SignalMeta = Pick<Signal, 'name' | 'unit'> & { label?: string; enum?: Record<string, string> }

export function signalLabel(s: SignalMeta): string {
  return s.label ?? s.name
}

/** Unit conversions for display. Only units whose meaning the dictionary states are converted. */
const conversions: Record<string, { unit: string; convert: (n: number) => number; digits: number }> = {
  dK: { unit: '°C', convert: (n) => n / 10 - 273.15, digits: 1 },
}

/** The unit a signal is displayed in, after conversion. */
export function displayUnit(unit: string | null | undefined): string {
  if (!unit) return ''
  return conversions[unit]?.unit ?? unit
}

/** Converts a numeric value into the display unit. */
export function displayNumber(unit: string | null | undefined, n: number): number {
  const c = unit ? conversions[unit] : undefined
  return c ? Math.round(c.convert(n) * 10 ** c.digits) / 10 ** c.digits : n
}

export interface Presented {
  text: string
  /** The decoded enum state, lowercase ("locked", "open"…), when the value is a documented code. */
  state?: string
  /** Service interval already passed. */
  overdue?: boolean
  muted?: boolean
}

/** Formats one value of a signal for people. */
export function presentValue(
  s: SignalMeta,
  v: { num?: number | null; text?: string | null; quality: string; unit?: string | null },
): Presented {
  if (v.quality !== 'ok') return { text: fmtValue(v), muted: true }
  const unit = v.unit ?? s.unit ?? ''
  if (v.num !== undefined && v.num !== null) {
    const code = Number.isInteger(v.num) ? s.enum?.[String(v.num)] : undefined
    if (code) return { text: capitalize(code), state: code }
    if (s.name.startsWith('maintenance_interval_') && v.num < 0) {
      return { text: `${fmtNumber(Math.abs(v.num))}${unitSuffix(unit)} overdue`, overdue: true }
    }
    const n = displayNumber(unit, v.num)
    return { text: `${fmtNumber(n)}${unitSuffix(displayUnit(unit))}` }
  }
  if (v.text === 'true' || v.text === 'false')
    return { text: v.text === 'true' ? 'On' : 'Off', state: v.text === 'true' ? 'on' : 'off' }
  return { text: fmtValue(v) }
}

function unitSuffix(unit: string): string {
  if (!unit) return ''
  return unit === '%' || unit === '°C' ? unit : ` ${unit === 'd' ? 'd' : unit}`
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/** The primary group of a signal: its first cluster other than the catch-all "All Data". */
export function signalGroup(s: Pick<Signal, 'clusters' | 'origin'>): string {
  const c = s.clusters.find((x) => x !== 'All Data')
  if (c) return c
  return s.origin === 'observed' ? 'Not in the dictionary' : 'Other'
}
