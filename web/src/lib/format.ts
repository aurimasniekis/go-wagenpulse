import { prefs } from './prefs'

/** Time zone used for display: the browser's zone or UTC (user preference). */
export function displayZone(): string {
  return prefs.state.timeZone === 'UTC' ? 'UTC' : Intl.DateTimeFormat().resolvedOptions().timeZone
}

const dtCache = new Map<string, Intl.DateTimeFormat>()

function dtf(opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const zone = displayZone()
  const key = zone + JSON.stringify(opts)
  let f = dtCache.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat(undefined, { ...opts, timeZone: zone })
    dtCache.set(key, f)
  }
  return f
}

export function toDate(v: string | number | Date | null | undefined): Date | null {
  if (v === null || v === undefined || v === '') return null
  const d = v instanceof Date ? v : new Date(v)
  return Number.isNaN(d.getTime()) ? null : d
}

/** "2026-11-02 14:05:09 CET" in the display zone. */
export function fmtDateTime(v: string | number | Date | null | undefined, withSeconds = true): string {
  const d = toDate(v)
  if (!d) return '—'
  return dtf({
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    ...(withSeconds ? { second: '2-digit' } : {}),
    hour12: false,
    timeZoneName: 'short',
  }).format(d)
}

export function fmtDate(v: string | number | Date | null | undefined): string {
  const d = toDate(v)
  return d ? dtf({ year: 'numeric', month: 'short', day: 'numeric' }).format(d) : '—'
}

/** ISO 8601 UTC, used in tooltips so the exact instant is always available. */
export function fmtISO(v: string | number | Date | null | undefined): string {
  const d = toDate(v)
  return d ? d.toISOString() : ''
}

/** "3 min ago", "in 12 min", "2 d ago". */
export function fmtRelative(v: string | number | Date | null | undefined, now: Date = new Date()): string {
  const d = toDate(v)
  if (!d) return '—'
  const s = Math.round((d.getTime() - now.getTime()) / 1000)
  const abs = Math.abs(s)
  const unit =
    abs < 60
      ? `${String(abs)} s`
      : abs < 3600
        ? `${String(Math.round(abs / 60))} min`
        : abs < 86400
          ? `${String(Math.round(abs / 3600))} h`
          : `${String(Math.round(abs / 86400))} d`
  if (abs < 5) return 'just now'
  return s < 0 ? `${unit} ago` : `in ${unit}`
}

/** Compact duration: 90 → "1 min 30 s", 7200 → "2 h". */
export function fmtDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds)) return '—'
  const s = Math.abs(Math.round(seconds))
  if (s < 60) return `${String(s)} s`
  if (s < 3600) {
    const m = Math.floor(s / 60)
    const r = s % 60
    return r ? `${String(m)} min ${String(r)} s` : `${String(m)} min`
  }
  if (s < 86400) {
    const h = Math.floor(s / 3600)
    const m = Math.round((s % 3600) / 60)
    return m ? `${String(h)} h ${String(m)} min` : `${String(h)} h`
  }
  const d = Math.floor(s / 86400)
  const h = Math.round((s % 86400) / 3600)
  return h ? `${String(d)} d ${String(h)} h` : `${String(d)} d`
}

const numFmt = new Intl.NumberFormat(undefined, { maximumFractionDigits: 3 })
const intFmt = new Intl.NumberFormat()

export function fmtNumber(n: number | null | undefined, digits?: number): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return '—'
  if (digits !== undefined) return new Intl.NumberFormat(undefined, { maximumFractionDigits: digits }).format(n)
  return Number.isInteger(n) ? intFmt.format(n) : numFmt.format(n)
}

export function fmtBytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${fmtNumber(v, i === 0 ? 0 : 1)} ${units[i] ?? 'B'}`
}

/** A typed value with its unit for display; non-ok qualities are spelled out. */
export function fmtValue(v: {
  num?: number | null
  text?: string | null
  quality: string
  unit?: string | null
}): string {
  switch (v.quality) {
    case 'null':
      return 'no value'
    case 'empty':
      return 'empty'
    case 'unset':
      return 'not available'
  }
  if (
    v.text !== undefined &&
    v.text !== null &&
    (v.num === undefined || v.num === null || v.text === 'true' || v.text === 'false')
  ) {
    return v.text
  }
  if (v.num !== undefined && v.num !== null) {
    const unit = v.unit ? (v.unit === '%' ? '%' : ` ${v.unit}`) : ''
    return `${fmtNumber(v.num)}${unit}`
  }
  return v.text ?? '—'
}

/** Masks a VIN except its last six characters (VINs are personal data). */
export function maskVIN(vin: string, reveal = false): string {
  if (reveal || vin.length <= 6) return vin
  return '•'.repeat(vin.length - 6) + vin.slice(-6)
}

/** Humanizes snake_case identifiers. */
export function humanize(s: string): string {
  const t = s.replace(/[_.]+/g, ' ').trim()
  return t.charAt(0).toUpperCase() + t.slice(1)
}
