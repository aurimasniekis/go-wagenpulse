import { notifications } from '@mantine/notifications'
import { ApiError } from '@/api/client'

export function notifySuccess(message: string, title?: string) {
  notifications.show({ color: 'green', title, message, autoClose: 4000 })
}

export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.fields.length > 0) return `${err.message} — ${err.fields.map((f) => `${f.field}: ${f.message}`).join('; ')}`
    return err.message
  }
  if (err instanceof Error) return err.message
  return 'Something went wrong'
}

export function notifyError(err: unknown, title = 'Request failed') {
  notifications.show({ color: 'red', title, message: errorMessage(err), autoClose: 8000 })
}

/** Field errors keyed by field name, for forms. */
export function fieldErrors(err: unknown): Record<string, string> {
  const out: Record<string, string> = {}
  if (err instanceof ApiError) {
    for (const f of err.fields) {
      const key = f.field.split('.').pop() ?? f.field
      out[key] ??= f.message
    }
  }
  return out
}
