import { displayNumber, displayUnit, presentValue, signalGroup, signalLabel } from './signals'

const lock = {
  name: 'locked_state_tailgate',
  unit: '',
  label: 'Tailgate lock',
  enum: { '0': 'unsupported', '1': 'invalid', '2': 'locked', '3': 'unlocked' },
}

describe('presentValue', () => {
  it('decodes documented enum codes', () => {
    expect(presentValue(lock, { num: 2, quality: 'ok' })).toEqual({ text: 'Locked', state: 'locked' })
    expect(presentValue(lock, { num: 3, quality: 'ok' }).text).toBe('Unlocked')
  })
  it('shows undocumented codes as numbers', () => {
    expect(presentValue(lock, { num: 7, quality: 'ok' }).text).toBe('7')
  })
  it('converts deci-Kelvin to °C', () => {
    const t = { name: 'outside_temperature', unit: 'dK' }
    expect(presentValue(t, { num: 2881, quality: 'ok', unit: 'dK' }).text).toBe('15°C')
    expect(displayUnit('dK')).toBe('°C')
    expect(displayNumber('dK', 2731.5)).toBe(0)
  })
  it('shows passed service intervals as overdue', () => {
    const s = { name: 'maintenance_interval__time_until_inspection', unit: 'd' }
    expect(presentValue(s, { num: -80, quality: 'ok', unit: 'd' })).toEqual({ text: '80 d overdue', overdue: true })
    expect(presentValue(s, { num: 30, quality: 'ok', unit: 'd' }).text).toBe('30 d')
  })
  it('keeps non-ok qualities muted', () => {
    expect(presentValue(lock, { quality: 'null' })).toMatchObject({ text: 'no value', muted: true })
  })
  it('renders booleans as On/Off', () => {
    expect(presentValue({ name: 'climacontrol', unit: '' }, { text: 'false', quality: 'ok' }).text).toBe('Off')
  })
})

describe('labels and groups', () => {
  it('prefers the dictionary label', () => {
    expect(signalLabel(lock)).toBe('Tailgate lock')
    expect(signalLabel({ name: 'raw_name', unit: '' })).toBe('raw_name')
  })
  it('groups by the first specific cluster', () => {
    expect(signalGroup({ clusters: ['All Data', 'Vehicle Access'], origin: 'continuous' })).toBe('Vehicle Access')
    expect(signalGroup({ clusters: [], origin: 'observed' })).toBe('Not in the dictionary')
  })
})
