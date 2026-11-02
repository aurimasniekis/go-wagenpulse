import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { request, type APIRequestContext } from '@playwright/test'
import { authDir, baseURL, bin, credsFile, deliveryFile, stateFile } from './env'
import { signals, SYNTHETIC_VIN, syntheticDelivery } from './fixtures'

/**
 * Bootstraps the fresh instance through its public interfaces, like an operator would:
 * `wagenpulse admin setup-token` → POST /api/v1/setup (root user) → a synthetic vehicle, an uploaded synthetic delivery
 * and a rule. The root session is saved for every test.
 */
export default async function globalSetup() {
  mkdirSync(authDir, { recursive: true })
  const api = await request.newContext({ baseURL, extraHTTPHeaders: { Origin: baseURL } })
  const out = execFileSync(bin, ['admin', 'setup-token'], { encoding: 'utf8' })
  const token = /wgp_setup_\S+/.exec(out)?.[0]
  if (!token) throw new Error(`no setup token in output:\n${out}`)
  const password = `e2e-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`
  const me = await call<{ csrf_token: string }>(api, 'POST', '/setup', {
    token,
    username: 'root',
    display_name: 'E2E Root',
    password,
  })
  writeFileSync(credsFile, JSON.stringify({ username: 'root', password }))
  const csrf = me.csrf_token

  const vehicle = await call<{ id: string }>(
    api,
    'POST',
    '/vehicles',
    { display_name: 'Test ID.4', vin: SYNTHETIC_VIN, brand: 'volkswagen', model: 'ID.4 (synthetic)', model_year: 2024 },
    csrf,
  )
  const delivery = syntheticDelivery()
  writeFileSync(deliveryFile, delivery)
  const upload = await api.post('/api/v1/uploads/deliveries', {
    headers: { 'X-CSRF-Token': csrf },
    multipart: {
      vehicle_id: vehicle.id,
      event_policy: 'preview',
      files: {
        name: 'synthetic-delivery.json',
        mimeType: 'application/json',
        buffer: Buffer.from(delivery),
      },
    },
  })
  if (!upload.ok()) throw new Error(`upload: ${String(upload.status())} ${await upload.text()}`)
  const im = (await upload.json()) as { id: string }
  for (let i = 0; i < 100; i++) {
    const s = await call<{ import: { state: string } }>(api, 'GET', `/imports/${im.id}`)
    if (!['queued', 'running'].includes(s.import.state)) {
      if (s.import.state !== 'succeeded') throw new Error(`import ended ${s.import.state}`)
      break
    }
    await new Promise((r) => setTimeout(r, 200))
  }
  await call(
    api,
    'POST',
    '/rules',
    {
      name: 'Battery low (synthetic)',
      description: 'E2E rule',
      enabled: true,
      definition: {
        kind: 'threshold',
        signal_keys: [signals.soc],
        vehicle_ids: [],
        severity: 'warning',
        direction: 'below',
        threshold: 50,
        hysteresis: 5,
      },
    },
    csrf,
  )
  await api.storageState({ path: stateFile })
  await api.dispose()
}

async function call<T = unknown>(
  api: APIRequestContext,
  method: string,
  path: string,
  data?: unknown,
  csrf = '',
): Promise<T> {
  const res = await api.fetch(`/api/v1${path}`, { method, data, headers: csrf ? { 'X-CSRF-Token': csrf } : {} })
  if (!res.ok()) throw new Error(`${method} ${path}: ${String(res.status())} ${await res.text()}`)
  return (res.status() === 204 ? {} : await res.json()) as T
}
