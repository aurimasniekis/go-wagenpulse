import createClient, { type Middleware } from 'openapi-fetch'
import type { components, paths } from './schema.gen'

/** Header carrying the session's CSRF token on state-changing cookie-authenticated requests. */
export const CSRF_HEADER = 'X-CSRF-Token'

let csrf = ''
let onUnauthorized: () => void = defaultUnauthorized

export function setCsrfToken(token: string | null | undefined) {
  csrf = token ?? ''
}

export function setUnauthorizedHandler(h: () => void) {
  onUnauthorized = h
}

/** Paths that render without a session. */
export const PUBLIC_PATHS = ['/login', '/setup']

export function isPublicPath(path: string) {
  return PUBLIC_PATHS.includes(path)
}

export function loginUrl(returnTo: string, reason?: string) {
  const q = new URLSearchParams()
  if (returnTo && returnTo !== '/') q.set('return_to', returnTo)
  if (reason) q.set('reason', reason)
  const s = q.toString()
  return s ? `/login?${s}` : '/login'
}

function defaultUnauthorized() {
  if (!isPublicPath(window.location.pathname)) {
    window.location.assign(loginUrl(`${window.location.pathname}${window.location.search}`, 'expired'))
  }
}

const mutating = (m: string) => !['GET', 'HEAD', 'OPTIONS'].includes(m.toUpperCase())

const sessionMiddleware: Middleware = {
  onRequest({ request }) {
    if (mutating(request.method) && csrf) request.headers.set(CSRF_HEADER, csrf)
    return request
  },
  onResponse({ request, response }) {
    const url = new URL(request.url)
    if (response.status === 401 && !url.pathname.startsWith('/api/v1/auth/') && url.pathname !== '/api/v1/setup') {
      onUnauthorized()
    }
    return response
  },
}

/** Typed client of /api/v1 (types generated from api/openapi.json by `make api`). */
export const api = createClient<paths>({ baseUrl: '/api/v1' })
api.use(sessionMiddleware)

export type Schemas = components['schemas']
export type Me = Schemas['Me']
export type User = Schemas['User']
export type Vehicle = Schemas['Vehicle']
export type VehicleSummary = Schemas['VehicleSummary']
export type StateValue = Schemas['StateValue']
export type Signal = Schemas['Signal']
export type Series = Schemas['Series']
export type Source = Schemas['Source']
export type Import = Schemas['Import']
export type RawDelivery = Schemas['RawDelivery']
export type Rule = Schemas['Rule']
export type Definition = Schemas['Definition']
export type Adapter = Schemas['Adapter']
export type Route = Schemas['Route']
export type EventItem = Schemas['Event']
export type Delivery = Schemas['Delivery']
export type Token = Schemas['Token']
export type Provider = Schemas['Provider']
export type SystemSettings = Schemas['System']
export type Retention = Schemas['Retention']
export type AuditRecord = Schemas['AuditRecord']
export type Overview = Schemas['Overview']

/** A field-level validation problem. */
export interface FieldProblem {
  field: string
  message: string
}

/** An API error with HTTP status, stable code and field problems. */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code = '',
    readonly fields: FieldProblem[] = [],
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

interface ProblemBody {
  title?: string
  detail?: string
  code?: string
  fields?: FieldProblem[]
  errors?: { message?: string; location?: string }[]
}

export function apiError(body: unknown, status: number): ApiError {
  const p = (body ?? {}) as ProblemBody
  const extra = (p.errors ?? [])
    .map((e) => [e.location, e.message].filter(Boolean).join(': '))
    .filter(Boolean)
    .join('; ')
  const msg = p.detail ?? p.title ?? `HTTP ${String(status)}`
  return new ApiError(extra ? `${msg} (${extra})` : msg, status, p.code ?? '', p.fields ?? [])
}

/** Unwraps an openapi-fetch result or throws an ApiError. */
export function unwrap<T>(res: { data?: T; error?: unknown; response: Response }): T {
  if (res.error !== undefined || res.data === undefined) throw apiError(res.error, res.response.status)
  return res.data
}

/** Throws for failed results without a body (204). */
export function check(res: { error?: unknown; response: Response }): void {
  if (res.error !== undefined || !res.response.ok) throw apiError(res.error, res.response.status)
}

/** Reads an ETag response header. */
export function etagOf(res: { response: Response }): string {
  return res.response.headers.get('ETag') ?? ''
}

/** Uploads files with multipart/form-data (outside the typed client: a streaming endpoint). */
export async function uploadDeliveries(
  files: File[],
  vehicleId: string,
  eventPolicy: string,
): Promise<Schemas['Import']> {
  const fd = new FormData()
  if (vehicleId) fd.append('vehicle_id', vehicleId)
  if (eventPolicy) fd.append('event_policy', eventPolicy)
  for (const f of files) fd.append('files', f, f.name)
  const res = await fetch('/api/v1/uploads/deliveries', { method: 'POST', body: fd, headers: { [CSRF_HEADER]: csrf } })
  const body: unknown = await res.json().catch(() => ({}))
  if (!res.ok) {
    if (res.status === 401) onUnauthorized()
    throw apiError(body, res.status)
  }
  return body as Schemas['Import']
}

/** Builds the CSV export URL (authenticated by the session cookie). */
export function exportUrl(vehicleIds: string[], signalKeys: string[], from: Date, to: Date): string {
  const q = new URLSearchParams()
  vehicleIds.forEach((v) => {
    q.append('vehicle', v)
  })
  signalKeys.forEach((s) => {
    q.append('signal', s)
  })
  q.set('from', from.toISOString())
  q.set('to', to.toISOString())
  return `/api/v1/exports/observations.csv?${q.toString()}`
}

/** If-Match header value for an object's revision (optimistic concurrency; a stale revision fails with 412). */
export function ifMatch(revision: number): { 'If-Match': string } {
  return { 'If-Match': `"${String(revision)}"` }
}

/** Fresh Idempotency-Key for create requests, so a retried submit cannot create a duplicate. */
export function idempotencyKey(): { 'Idempotency-Key': string } {
  return { 'Idempotency-Key': crypto.randomUUID() }
}

/** Posts a raw JSON document to a streaming endpoint outside the typed client (dictionary uploads). */
export async function uploadDictionary(file: File): Promise<unknown> {
  const res = await fetch('/api/v1/uploads/dictionary', {
    method: 'POST',
    body: file,
    headers: { 'Content-Type': 'application/json', [CSRF_HEADER]: csrf },
  })
  const body: unknown = await res.json().catch(() => ({}))
  if (!res.ok) {
    if (res.status === 401) onUnauthorized()
    throw apiError(body, res.status)
  }
  return body
}
