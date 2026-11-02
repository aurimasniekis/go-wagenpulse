import { Store } from '@tanstack/react-store'

/**
 * Per-browser UI preferences (TanStack Store). Server state lives in TanStack Query; only presentation choices are
 * kept here. Persistence is best effort: storage may be unavailable (private windows, blocked site data).
 */
export interface Prefs {
  timeZone: 'local' | 'UTC'
}

const STORAGE_ITEM = 'wagenpulse.prefs.v1'

function load(): Prefs {
  const defaults: Prefs = { timeZone: 'local' }
  try {
    const raw = window.localStorage.getItem(STORAGE_ITEM)
    if (!raw) return defaults
    const v = JSON.parse(raw) as Partial<Prefs>
    return {
      timeZone: v.timeZone === 'UTC' ? 'UTC' : 'local',
    }
  } catch {
    return defaults
  }
}

export const prefs = new Store<Prefs>(load())

prefs.subscribe(() => {
  try {
    window.localStorage.setItem(STORAGE_ITEM, JSON.stringify(prefs.state))
  } catch {
    // Storage unavailable: preferences last for this page only.
  }
})

export function setPref<K extends keyof Prefs>(key: K, value: Prefs[K]) {
  prefs.setState((s) => ({ ...s, [key]: value }))
}
