import {
  Alert,
  Badge,
  Button,
  Card,
  Group,
  Modal,
  MultiSelect,
  Select,
  SimpleGrid,
  Stack,
  Tabs,
  Text,
  TextInput,
} from '@mantine/core'
import { IconHistory, IconRefresh } from '@tabler/icons-react'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { api, unwrap, type Delivery, type EventItem, type Schemas } from '@/api/client'
import { DataTable, Dim } from '@/components/DataTable'
import { DeliveryCounts, eventTypes } from '@/components/domain'
import { AnchorLink } from '@/components/links'
import { EmptyBlock, PageHeader, QueryState, StatusBadge, Time } from '@/components/ui'
import { fmtNumber } from '@/lib/format'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'

interface Search {
  tab?: 'events' | 'deliveries'
  vehicle?: string
  rule?: string
  type?: string
  adapter?: string
  state?: string
}

const str = (v: unknown) => (typeof v === 'string' && v ? v : undefined)

export const Route = createFileRoute('/events/')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    tab: s.tab === 'deliveries' ? 'deliveries' : undefined,
    vehicle: str(s.vehicle),
    rule: str(s.rule),
    type: str(s.type),
    adapter: str(s.adapter),
    state: str(s.state),
  }),
  component: Events,
})

function useLookups() {
  const can = useCan()
  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
  })
  const rules = useQuery({
    queryKey: ['rules'],
    queryFn: async () => unwrap(await api.GET('/rules')),
    enabled: can('rules.read'),
  })
  const adapters = useQuery({
    queryKey: ['adapters'],
    queryFn: async () => unwrap(await api.GET('/adapters')),
    enabled: can('adapters.read'),
  })
  return {
    vehicles: (vehicles.data?.items ?? []).map((v) => ({ value: v.id, label: v.display_name })),
    rules: (rules.data ?? []).map((r) => ({ value: r.id, label: r.name })),
    adapters: (adapters.data ?? []).map((a) => ({ value: a.id, label: a.name })),
  }
}

function Events() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/events/' })
  const can = useCan()
  const [replay, setReplay] = useState(false)
  const tab = search.tab ?? 'events'
  const set = (p: Partial<Search>) => void navigate({ search: (s) => ({ ...s, ...p }), replace: true })
  return (
    <>
      <PageHeader
        title="Events & deliveries"
        description="Events are created by rules; each matching route creates one delivery per adapter. Delivered means JetStream stored the event."
        actions={
          can('deliveries.replay') ? (
            <Button
              variant="default"
              leftSection={<IconHistory size={16} />}
              onClick={() => {
                setReplay(true)
              }}
            >
              Replay events…
            </Button>
          ) : null
        }
      />
      <Tabs
        value={tab}
        onChange={(t) => void navigate({ search: { tab: t === 'deliveries' ? 'deliveries' : undefined } })}
      >
        <Tabs.List mb="md">
          <Tabs.Tab value="events">Events</Tabs.Tab>
          <Tabs.Tab value="deliveries">Deliveries</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="events">
          <EventList search={search} set={set} />
        </Tabs.Panel>
        <Tabs.Panel value="deliveries">
          <DeliveryList search={search} set={set} />
        </Tabs.Panel>
      </Tabs>
      <ReplayDialog
        opened={replay}
        onClose={() => {
          setReplay(false)
        }}
      />
    </>
  )
}

function EventList({ search, set }: { search: Search; set: (p: Partial<Search>) => void }) {
  const lk = useLookups()
  const navigate = useNavigate()
  const list = useInfiniteQuery({
    queryKey: ['events', 'list', search.vehicle, search.rule, search.type],
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET('/events', {
          params: {
            query: {
              vehicle: search.vehicle,
              rule: search.rule,
              type: search.type,
              limit: 100,
              before: pageParam || undefined,
            },
          },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (last) => last.next_before,
    refetchInterval: 30_000,
  })
  const vName = (id?: string) => lk.vehicles.find((v) => v.value === id)?.label ?? id ?? '—'
  const rName = (id?: string) => lk.rules.find((v) => v.value === id)?.label ?? id ?? '—'
  const columns = useMemo<ColumnDef<EventItem>[]>(
    () => [
      {
        header: 'Event',
        accessorKey: 'type',
        cell: ({ row }) => (
          <Group gap={6} wrap="nowrap">
            <AnchorLink to="/events/$eventId" params={{ eventId: row.original.id }} size="sm" fw={550}>
              {row.original.type}
            </AnchorLink>
            {row.original.backfill ? (
              <Badge size="xs" variant="light" color="gray">
                backfill
              </Badge>
            ) : null}
            {row.original.payload?.severity && row.original.payload.severity !== 'info' ? (
              <Badge size="xs" variant="light" color={row.original.payload.severity === 'critical' ? 'red' : 'yellow'}>
                {row.original.payload.severity}
              </Badge>
            ) : null}
          </Group>
        ),
      },
      { header: 'Vehicle', accessorFn: (e) => vName(e.vehicle_id) },
      {
        header: 'Rule',
        accessorFn: (e) => rName(e.rule_id),
        cell: ({ row }) =>
          row.original.rule_id ? (
            `${rName(row.original.rule_id)} (r${String(row.original.rule_revision ?? '')})`
          ) : (
            <Dim>—</Dim>
          ),
      },
      {
        header: 'Signal',
        accessorFn: (e) => e.payload?.signal?.name ?? '',
        cell: ({ getValue }) => (getValue() as string) || <Dim>—</Dim>,
      },
      { header: 'Observed', accessorKey: 'observed_at', cell: ({ row }) => <Time value={row.original.observed_at} /> },
      {
        header: 'Deliveries',
        id: 'del',
        enableSorting: false,
        cell: ({ row }) => <DeliveryCounts d={row.original.deliveries} />,
      },
    ],
    // eslint-disable-next-line react-hooks/exhaustive-deps -- lookups only change labels
    [lk.vehicles.length, lk.rules.length],
  )
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <Card p={0}>
      <Group p="md" gap="sm" wrap="wrap">
        <Select
          placeholder="Any vehicle"
          aria-label="Vehicle"
          data={lk.vehicles}
          value={search.vehicle ?? null}
          onChange={(v) => {
            set({ vehicle: v ?? undefined })
          }}
          clearable
          searchable
          w={200}
        />
        <Select
          placeholder="Any rule"
          aria-label="Rule"
          data={lk.rules}
          value={search.rule ?? null}
          onChange={(v) => {
            set({ rule: v ?? undefined })
          }}
          clearable
          searchable
          w={220}
        />
        <Select
          placeholder="Any type"
          aria-label="Event type"
          data={eventTypes}
          value={search.type ?? null}
          onChange={(v) => {
            set({ type: v ?? undefined })
          }}
          clearable
          w={200}
        />
      </Group>
      <QueryState query={list} loadingLabel="Loading events…">
        {() => (
          <>
            <DataTable
              caption="Events"
              testId="events-table"
              data={items}
              columns={columns}
              onRowClick={(e) => void navigate({ to: '/events/$eventId', params: { eventId: e.id } })}
              empty={
                <EmptyBlock
                  title="No events"
                  description="Events appear when rules match imported or collected observations."
                />
              }
            />
            {list.hasNextPage ? (
              <Group justify="center" p="md">
                <Button variant="default" loading={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                  Load older events
                </Button>
              </Group>
            ) : null}
          </>
        )}
      </QueryState>
    </Card>
  )
}

function DeliveryList({ search, set }: { search: Search; set: (p: Partial<Search>) => void }) {
  const lk = useLookups()
  const can = useCan()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const list = useInfiniteQuery({
    queryKey: ['deliveries', 'list', search.adapter, search.state],
    queryFn: async ({ pageParam }) =>
      unwrap(
        await api.GET('/deliveries', {
          params: {
            query: {
              adapter: search.adapter,
              state: search.state as Delivery['state'] | undefined,
              limit: 100,
              before: pageParam || undefined,
            },
          },
        }),
      ),
    initialPageParam: 0,
    getNextPageParam: (last) => last.next_before,
    refetchInterval: 15_000,
  })
  const retry = useMutation({
    mutationFn: async (id: string) => unwrap(await api.POST('/deliveries/{id}/retry', { params: { path: { id } } })),
    onSuccess: async () => {
      notifySuccess('Delivery queued for another attempt')
      await qc.invalidateQueries({ queryKey: ['deliveries'] })
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const columns = useMemo<ColumnDef<Delivery>[]>(
    () => [
      {
        header: 'Delivery',
        accessorKey: 'event_type',
        cell: ({ row }) => (
          <div>
            <AnchorLink to="/deliveries/$deliveryId" params={{ deliveryId: row.original.id }} size="sm" fw={550}>
              {row.original.event_type}
            </AnchorLink>
            {row.original.replay_seq > 0 ? (
              <Badge size="xs" ml={6} variant="light" color="grape">
                replay {row.original.replay_seq}
              </Badge>
            ) : null}
            <Text size="xs" c="dimmed" ff="monospace" lineClamp={1} maw={360}>
              {row.original.subject ?? ''}
            </Text>
          </div>
        ),
      },
      { header: 'Adapter', accessorKey: 'adapter_name' },
      { header: 'State', accessorKey: 'state', cell: ({ row }) => <StatusBadge state={row.original.state} /> },
      {
        header: 'Attempts',
        accessorFn: (d) => d.attempts,
        cell: ({ row }) => `${String(row.original.attempts)}/${String(row.original.max_attempts)}`,
      },
      {
        header: 'Last error',
        accessorFn: (d) => d.last_error_class ?? '',
        cell: ({ row }) =>
          row.original.last_error_class ? <Text size="xs">{row.original.last_error_class}</Text> : <Dim>—</Dim>,
      },
      {
        header: 'Next / done',
        accessorFn: (d) => d.next_attempt_at ?? d.succeeded_at ?? '',
        cell: ({ row }) =>
          row.original.state === 'succeeded' ? (
            <Time value={row.original.succeeded_at} relative />
          ) : row.original.next_attempt_at && row.original.state !== 'dead' ? (
            <Time value={row.original.next_attempt_at} relative />
          ) : (
            <Dim>{row.original.dead_reason ?? '—'}</Dim>
          ),
      },
      {
        id: 'act',
        header: '',
        enableSorting: false,
        cell: ({ row }) =>
          can('deliveries.retry') && (row.original.state === 'dead' || row.original.state === 'retrying') ? (
            <Button
              size="xs"
              variant="default"
              leftSection={<IconRefresh size={14} />}
              onClick={(e) => {
                e.stopPropagation()
                retry.mutate(row.original.id)
              }}
            >
              Retry now
            </Button>
          ) : null,
      },
    ],
    [can, retry],
  )
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <Card p={0}>
      <Group p="md" gap="sm">
        <Select
          placeholder="Any adapter"
          aria-label="Adapter"
          data={lk.adapters}
          value={search.adapter ?? null}
          onChange={(v) => {
            set({ adapter: v ?? undefined })
          }}
          clearable
          w={220}
        />
        <Select
          placeholder="Any state"
          aria-label="Delivery state"
          data={['pending', 'processing', 'retrying', 'succeeded', 'dead']}
          value={search.state ?? null}
          onChange={(v) => {
            set({ state: v ?? undefined })
          }}
          clearable
          w={180}
        />
      </Group>
      <QueryState query={list} loadingLabel="Loading deliveries…">
        {() => (
          <>
            <DataTable
              caption="Deliveries"
              testId="deliveries-table"
              data={items}
              columns={columns}
              onRowClick={(d) => void navigate({ to: '/deliveries/$deliveryId', params: { deliveryId: d.id } })}
              empty={
                <EmptyBlock title="No deliveries" description="Deliveries are created when an event matches a route." />
              }
            />
            {list.hasNextPage ? (
              <Group justify="center" p="md">
                <Button variant="default" loading={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                  Load older deliveries
                </Button>
              </Group>
            ) : null}
          </>
        )}
      </QueryState>
    </Card>
  )
}

function toLocalInput(d: Date) {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${String(d.getFullYear())}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** Controlled replay: a dry count first, then an explicit confirmation that creates new deliveries. */
function ReplayDialog({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const lk = useLookups()
  const qc = useQueryClient()
  const [adapter, setAdapter] = useState<string | null>(null)
  const [rule, setRule] = useState<string | null>(null)
  const [vehicles, setVehicles] = useState<string[]>([])
  const [types, setTypes] = useState<string[]>([])
  const [from, setFrom] = useState(() => toLocalInput(new Date(Date.now() - 86400_000)))
  const [to, setTo] = useState(() => toLocalInput(new Date()))
  const [count, setCount] = useState<Schemas['ReplayResult'] | null>(null)
  const body = (confirm: boolean): Schemas['Replay-eventsRequest'] => ({
    adapter_id: adapter ?? '',
    rule_id: rule ?? undefined,
    vehicle_ids: vehicles,
    types,
    from: new Date(from).toISOString(),
    to: new Date(to).toISOString(),
    confirm,
  })
  const dry = useMutation({
    mutationFn: async () => unwrap(await api.POST('/replays', { body: body(false) })),
    onSuccess: setCount,
    onError: (e) => {
      notifyError(e)
    },
  })
  const run = useMutation({
    mutationFn: async () => unwrap(await api.POST('/replays', { body: body(true) })),
    onSuccess: async (r) => {
      notifySuccess(`${fmtNumber(r.deliveries_created)} replay deliveries created`)
      await qc.invalidateQueries({ queryKey: ['deliveries'] })
      setCount(null)
      onClose()
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const reset = () => {
    setCount(null)
  }
  return (
    <Modal
      opened={opened}
      onClose={() => {
        reset()
        onClose()
      }}
      title={<Text fw={650}>Replay events</Text>}
      size="lg"
    >
      <Stack gap="sm">
        <Text size="sm" c="dimmed">
          Re-sends stored events to one adapter as new deliveries. Consumers receive them again (with a replay marker);
          this is at-least-once delivery, so consumers must tolerate duplicates.
        </Text>
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <Select
            label="Adapter"
            withAsterisk
            data={lk.adapters}
            value={adapter}
            onChange={(v) => {
              setAdapter(v)
              reset()
            }}
          />
          <Select
            label="Rule"
            placeholder="Any rule"
            data={lk.rules}
            value={rule}
            onChange={(v) => {
              setRule(v)
              reset()
            }}
            clearable
            searchable
          />
          <TextInput
            label="From"
            type="datetime-local"
            value={from}
            onChange={(e) => {
              setFrom(e.currentTarget.value)
              reset()
            }}
          />
          <TextInput
            label="To"
            type="datetime-local"
            value={to}
            onChange={(e) => {
              setTo(e.currentTarget.value)
              reset()
            }}
          />
        </SimpleGrid>
        <MultiSelect
          label="Vehicles"
          placeholder={vehicles.length ? undefined : 'All vehicles'}
          data={lk.vehicles}
          value={vehicles}
          onChange={(v) => {
            setVehicles(v)
            reset()
          }}
          searchable
        />
        <MultiSelect
          label="Event types"
          placeholder={types.length ? undefined : 'All types'}
          data={eventTypes}
          value={types}
          onChange={(v) => {
            setTypes(v)
            reset()
          }}
        />
        {count ? (
          <Alert variant="light" color={count.events > 0 ? 'blue' : 'gray'}>
            {count.events > 0
              ? `${fmtNumber(count.events)} event(s) match and would be delivered again to this adapter (limit ${fmtNumber(count.limit)} per replay).`
              : 'No event matches these filters.'}
          </Alert>
        ) : null}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            Cancel
          </Button>
          {count && count.events > 0 ? (
            <Button
              color="orange"
              loading={run.isPending}
              onClick={() => {
                run.mutate()
              }}
            >
              Replay {fmtNumber(Math.min(count.events, count.limit))} event(s)
            </Button>
          ) : (
            <Button
              loading={dry.isPending}
              disabled={!adapter || !from || !to}
              onClick={() => {
                dry.mutate()
              }}
            >
              Count matching events
            </Button>
          )}
        </Group>
      </Stack>
    </Modal>
  )
}
