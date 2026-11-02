import { Store } from '@tanstack/react-store'

/**
 * Per-browser UI preferences (TanStack Store). Server state lives in TanStack Query; only presentation choices are
 * kept here. Persistence is best effort: storage may be unavailable (private windows, blocked site data).
 */
export interface Prefs {
  timeZone: 'local' | 'UTC'
  revealVIN: boolean
  navCollapsed: boolean
}

const KEY = 'wagenpulse.prefs.v1'

function load(): Prefs {
  const defaults: Prefs = { timeZone: 'local', revealVIN: false, navCollapsed: false }
  try {
    const raw = window.localStorage.getItem(KEY)
    if (!raw) return defaults
    const v = JSON.parse(raw) as Partial<Prefs>
    return {
      timeZone: v.timeZone === 'UTC' ? 'UTC' : 'local',
      revealVIN: v.revealVIN === true,
      navCollapsed: v.navCollapsed === true,
    }
  } catch {
    return defaults
  }
}

export const prefs = new Store<Prefs>(load())

prefs.subscribe(() => {
  try {
    window.localStorage.setItem(KEY, JSON.stringify(prefs.state))
  } catch {
    // Storage unavailable: preferences last for this page only.
  }
})

export function setPref<K extends keyof Prefs>(key: K, value: Prefs[K]) {
  prefs.setState((s) => ({ ...s, [key]: value }))
}
