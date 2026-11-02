import { MultiSelect } from '@mantine/core'
import { useDebouncedValue } from '@mantine/hooks'
import { keepPreviousData, useQueries, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, unwrap, type Signal } from '@/api/client'
import { signalLabel } from '@/lib/signals'

/**
 * Multi-select over the signal dictionary: signals that have data come first; typing searches the whole dictionary
 * (both bundled dictionaries and observed keys). Selected keys outside the loaded lists are resolved individually.
 */
export function SignalPicker({
  value,
  onChange,
  label,
  description,
  error,
  max = 50,
}: {
  value: string[]
  onChange: (v: string[]) => void
  label: string
  description?: string
  error?: string
  max?: number
}) {
  const [search, setSearch] = useState('')
  const [q] = useDebouncedValue(search, 250)
  const seen = useQuery({
    queryKey: ['signals', 'seen'],
    queryFn: async () => unwrap(await api.GET('/signals', { params: { query: { only_seen: true, limit: 200 } } })),
  })
  const found = useQuery({
    queryKey: ['signals', 'search', q],
    queryFn: async () => unwrap(await api.GET('/signals', { params: { query: { q, limit: 50 } } })),
    enabled: q.trim().length >= 2,
    placeholderData: keepPreviousData,
  })
  const known = new Map<string, Signal>()
  for (const s of [...(seen.data?.items ?? []), ...(q.trim().length >= 2 ? (found.data?.items ?? []) : [])])
    known.set(s.key, s)
  const missing = value.filter((k) => !known.has(k))
  const resolved = useQueries({
    queries: missing.map((k) => ({
      queryKey: ['signal', k],
      queryFn: async () => unwrap(await api.GET('/signals/{key}', { params: { path: { key: k } } })),
      staleTime: 300_000,
    })),
  })
  for (const r of resolved) if (r.data) known.set(r.data.key, r.data)
  const foundKeys = new Set(q.trim().length >= 2 ? found.data?.items.map((s) => s.key) : [])
  const seenKeys = new Set(seen.data?.items.map((s) => s.key))
  const data = [...known.values()].map((s) => ({
    value: s.key,
    label: `${signalLabel(s)}${s.unit ? ` (${s.unit})` : ''}${seenKeys.has(s.key) ? '' : ' · no data yet'}`,
  }))
  for (const k of value) if (!known.has(k)) data.push({ value: k, label: k })
  return (
    <MultiSelect
      label={label}
      description={description}
      error={error}
      data={data}
      value={value}
      onChange={onChange}
      searchable
      searchValue={search}
      onSearchChange={setSearch}
      maxValues={max}
      nothingFoundMessage={
        q.trim().length < 2 ? 'Type at least two characters to search the dictionary' : 'No signal matches'
      }
      placeholder={value.length ? undefined : 'Search signals'}
      filter={({ options, search: term }) => {
        const t = term.trim().toLowerCase()
        if (!t) return options
        return options.filter((o) => 'value' in o && (o.label.toLowerCase().includes(t) || foundKeys.has(o.value)))
      }}
      limit={100}
    />
  )
}
