import { join } from 'node:path'

/** The instance under test (scripts/e2e.sh starts a fresh one and exports these). */
export const baseURL = process.env.WGP_E2E_URL ?? 'http://localhost:8080'
export const bin = process.env.WGP_E2E_BIN ?? '../bin/wagenpulse'
export const authDir = process.env.WGP_E2E_AUTH_DIR ?? join(import.meta.dirname, '.auth')
export const stateFile = join(authDir, 'state.json')
export const credsFile = join(authDir, 'root.json')
/** The exact synthetic delivery global-setup uploaded (re-uploaded by the duplicate-detection test). */
export const deliveryFile = join(authDir, 'delivery.json')
