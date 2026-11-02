import { Badge, Button, Card, Code, Drawer, Group, Select, Stack, Switch, Text, TextInput } from '@mantine/core'
import { useDebouncedValue } from '@mantine/hooks'
import { IconSearch, IconUpload } from '@tabler/icons-react'
import type { ColumnDef } from '@tanstack/react-table'
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useRef, useState } from 'react'
import { api, unwrap, uploadDictionary, type Signal } from '@/api/client'
import { DataTable, Dim } from '@/components/DataTable'
import { DefinitionList, EmptyBlock, PageHeader, QueryState, Section, Time } from '@/components/ui'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'
import { AnchorLink } from '@/components/links'
import { signalLabel } from '@/lib/signals'

interface Search {
  q?: string
  cluster?: string
  origin?: string
  seen?: boolean
  key?: string
}

export const Route = createFileRoute('/signals')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    q: typeof s.q === 'string' ? s.q : undefined,
    cluster: typeof s.cluster === 'string' ? s.cluster : undefined,
    origin: typeof s.origin === 'string' ? s.origin : undefined,
    seen: s.seen === true || s.seen === 'true' ? true : undefined,
    key: typeof s.key === 'string' ? s.key : undefined,
  }),
  component: Signals,
})

const columns: ColumnDef<Signal>[] = [
  {
    header: 'Signal',
    accessorFn: (s) => signalLabel(s),
    cell: ({ row }) => (
      <div>
        <Text size="sm" fw={550}>
          {signalLabel(row.original)}
        </Text>
        <Text size="xs" c="dimmed" lineClamp={1} maw={520}>
          <Text span ff="monospace" size="xs">
            {row.original.name}
          </Text>
          {row.original.description ? ` · ${row.original.description}` : ''}
        </Text>
      </div>
    ),
  },
  { header: 'Type', accessorKey: 'value_kind' },
  { header: 'Unit', accessorFn: (s) => s.unit ?? '', cell: ({ getValue }) => (getValue() as string) || <Dim>—</Dim> },
  {
    header: 'Origin',
    accessorKey: 'origin',
    cell: ({ getValue }) => {
      const o = getValue() as string
      return (
        <Badge variant="light" color={o === 'observed' ? 'yellow' : 'gray'}>
          {o}
        </Badge>
      )
    },
  },
  {
    header: 'Last seen',
    accessorFn: (s) => s.last_seen_at ?? '',
    cell: ({ row }) =>
      row.original.last_seen_at ? <Time value={row.original.last_seen_at} relative /> : <Dim>never</Dim>,
  },
]

function Signals() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/signals' })
  const can = useCan()
  const qc = useQueryClient()
  const [text, setText] = useState(search.q ?? '')
  const [q] = useDebouncedValue(text, 250)
  const set = (p: Partial<Search>) => void navigate({ search: (s) => ({ ...s, ...p }), replace: true })
  const clusters = useQuery({
    queryKey: ['signal-clusters'],
    queryFn: async () => unwrap(await api.GET('/signal-clusters')),
  })
  const dicts = useQuery({ queryKey: ['dictionaries'], queryFn: async () => unwrap(await api.GET('/dictionaries')) })
  const list = useInfiniteQuery({
    queryKey: ['signals', 'search', q, search.cluster, search.origin, search.seen],
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET('/signals', {
          params: {
            query: {
              q: q || undefined,
              cluster: search.cluster,
              origin: search.origin as Signal['origin'] | undefined,
              only_seen: search.seen,
              limit: 100,
              cursor: pageParam || undefined,
            },
          },
        }),
      ),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor || undefined,
  })
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  const selected = items.find((s) => s.key === search.key)
  const fileRef = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)
  const upload = async (file: File) => {
    setUploading(true)
    try {
      await uploadDictionary(file)
      notifySuccess('Dictionary imported')
      await qc.invalidateQueries({ queryKey: ['signals'] })
      await qc.invalidateQueries({ queryKey: ['dictionaries'] })
    } catch (e) {
      notifyError(e, 'Dictionary import failed')
    } finally {
      setUploading(false)
    }
  }

  return (
    <>
      <PageHeader
        title="Signal dictionary"
        description="Signals from the bundled EU Data Act dictionaries, plus keys seen in deliveries that the dictionary does not describe."
        actions={
          can('dictionary.write') ? (
            <>
              <input
                ref={fileRef}
                type="file"
                accept="application/json,.json"
                hidden
                onChange={(e) => {
                  const f = e.currentTarget.files?.[0]
                  if (f) void upload(f)
                  e.currentTarget.value = ''
                }}
              />
              <Button
                variant="default"
                leftSection={<IconUpload size={16} />}
                loading={uploading}
                onClick={() => fileRef.current?.click()}
              >
                Import dictionary JSON
              </Button>
            </>
          ) : null
        }
      />
      {dicts.data && dicts.data.length > 0 ? (
        <Group mb="md" gap="xs">
          {dicts.data.map((d) => (
            <Badge key={d.id} variant="light" color="gray" tt="none" size="lg">
              {d.title} v{d.document_version} ({d.document_date}) · {d.entry_count.toLocaleString()} entries ·{' '}
              {d.origin}
            </Badge>
          ))}
        </Group>
      ) : null}
      <Card p={0}>
        <Group p="md" gap="sm" wrap="wrap">
          <TextInput
            leftSection={<IconSearch size={14} />}
            placeholder="Search name, description or key"
            value={text}
            onChange={(e) => {
              setText(e.currentTarget.value)
              set({ q: e.currentTarget.value || undefined })
            }}
            w={320}
            aria-label="Search signals"
          />
          <Select
            placeholder="All clusters"
            aria-label="Cluster"
            data={Object.entries(clusters.data ?? {})
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([c, n]) => ({ value: c, label: `${c} (${String(n)})` }))}
            value={search.cluster ?? null}
            onChange={(c) => {
              set({ cluster: c ?? undefined })
            }}
            clearable
            searchable
            w={280}
          />
          <Select
            placeholder="Any origin"
            aria-label="Origin"
            data={[
              { value: 'continuous', label: 'Continuous dictionary' },
              { value: 'historical', label: 'Historical dictionary' },
              { value: 'observed', label: 'Observed (not in dictionary)' },
            ]}
            value={search.origin ?? null}
            onChange={(o) => {
              set({ origin: o ?? undefined })
            }}
            clearable
            w={240}
          />
          <Switch
            label="Only signals with data"
            checked={Boolean(search.seen)}
            onChange={(e) => {
              set({ seen: e.currentTarget.checked || undefined })
            }}
          />
        </Group>
        <QueryState query={list} loadingLabel="Searching signals…">
          {() => (
            <>
              <DataTable
                caption="Signals"
                data={items}
                columns={columns}
                onRowClick={(s) => {
                  set({ key: s.key })
                }}
                empty={<EmptyBlock title="No signals match" />}
                testId="signals-table"
              />
              {list.hasNextPage ? (
                <Group justify="center" p="md">
                  <Button variant="default" loading={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                    Load more
                  </Button>
                </Group>
              ) : null}
            </>
          )}
        </QueryState>
      </Card>
      <Drawer
        opened={Boolean(search.key)}
        onClose={() => {
          set({ key: undefined })
        }}
        position="right"
        size="lg"
        title={<Text fw={650}>{selected ? signalLabel(selected) : 'Signal'}</Text>}
      >
        {selected ? <SignalDetail s={selected} /> : <SignalByKey k={search.key ?? ''} />}
      </Drawer>
    </>
  )
}

function SignalByKey({ k }: { k: string }) {
  const q = useQuery({
    queryKey: ['signal', k],
    queryFn: async () => unwrap(await api.GET('/signals/{key}', { params: { path: { key: k } } })),
    enabled: Boolean(k),
  })
  return <QueryState query={q}>{() => (q.data ? <SignalDetail s={q.data} /> : null)}</QueryState>
}

function SignalDetail({ s }: { s: Signal }) {
  return (
    <Stack>
      {s.description ? <Text size="sm">{s.description}</Text> : null}
      <Section title="Metadata">
        <DefinitionList
          items={[
            ['Key', <Code key="k">{s.key}</Code>],
            ['Origin', s.origin],
            ['Value type', s.value_kind],
            ['Dictionary data type', s.data_type || '—'],
            [
              'Unit',
              s.unit
                ? `${s.unit}${s.unit_raw && s.unit_raw !== s.unit ? ` (dictionary: ${s.unit_raw})` : ''}`
                : s.unit_raw || '—',
            ],
            ['Clusters', s.clusters.length ? s.clusters.join(', ') : '—'],
            [
              'Codes',
              s.enum
                ? Object.entries(s.enum)
                    .sort(([x], [y]) => Number(x) - Number(y))
                    .map(([k, v]) => `${k} = ${v}`)
                    .join(', ')
                : '—',
            ],
            ['Name in deliveries', s.observed_name || '—'],
            ['First seen', <Time key="f" value={s.first_seen_at} />],
            ['Last seen', <Time key="l" value={s.last_seen_at} />],
          ]}
        />
      </Section>
      {s.origin === 'observed' ? (
        <Text size="xs" c="dimmed">
          This key appeared in a delivery but is not described by any loaded dictionary. Its value is stored as
          received; no unit or type is invented.
        </Text>
      ) : null}
      {s.last_seen_at ? (
        <AnchorLink to="/explorer" search={{ signals: [s.key] }} size="sm">
          Explore this signal
        </AnchorLink>
      ) : null}
    </Stack>
  )
}
