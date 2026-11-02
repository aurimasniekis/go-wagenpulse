import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, setCsrfToken, unwrap, type Me } from '@/api/client'

export const meKey = ['me'] as const

/** Loads the signed-in principal (and its CSRF token). Resolves to null when not signed in. */
export async function fetchMe(): Promise<Me | null> {
  const res = await api.GET('/me')
  if (res.response.status === 401) {
    setCsrfToken('')
    return null
  }
  const me = unwrap(res)
  setCsrfToken(me.csrf_token)
  return me
}

export function useMe() {
  return useQuery({ queryKey: meKey, queryFn: fetchMe, staleTime: 60_000 })
}

/** Returns a permission checker for the signed-in principal. */
export function useCan(): (perm: string) => boolean {
  const { data } = useMe()
  const perms = new Set(data?.permissions ?? [])
  return (perm) => perms.has(perm)
}

export function useInvalidateMe() {
  const qc = useQueryClient()
  return () => qc.invalidateQueries({ queryKey: meKey })
}
