import {
  ActionIcon,
  Badge,
  Button,
  Card,
  Group,
  Menu,
  Modal,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Table,
  Tabs,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core'
import {
  IconArchive,
  IconDots,
  IconEye,
  IconEyeOff,
  IconPencil,
  IconRestore,
  IconSearch,
  IconTrash,
} from '@tabler/icons-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMemo, useState, type ReactNode } from 'react'
import { api, check, ifMatch, unwrap, type StateValue } from '@/api/client'
import { ConfirmDialog } from '@/components/Confirm'
import { DEFAULT_RANGE_MS, ranges, useRangeEnd, SeriesChart } from '@/components/SeriesChart'
import { DeliveryCounts } from '@/components/domain'
import { VehicleOverview } from '@/components/VehicleOverview'
import { presentValue, signalLabel } from '@/lib/signals'
import {
  DefinitionList,
  EmptyBlock,
  ErrorBlock,
  Kpi,
  LoadingBlock,
  PageHeader,
  QueryState,
  ScrollRegion,
  Section,
  StatusBadge,
  Time,
} from '@/components/ui'
import { brands, VehicleForm } from '@/components/VehicleForm'
import { fmtDuration, fmtNumber, maskVIN } from '@/lib/format'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useCan } from '@/lib/session'
import { AnchorLink } from '@/components/links'

interface Search {
  tab?: 'overview' | 'state' | 'history' | 'imports' | 'events' | 'details'
  signal?: string
  range?: string
}

export const Route = createFileRoute('/vehicles/$vehicleId')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    tab: ['overview', 'state', 'history', 'imports', 'events', 'details'].includes(s.tab as string)
      ? (s.tab as Search['tab'])
      : undefined,
    signal: typeof s.signal === 'string' ? s.signal : undefined,
    range: typeof s.range === 'string' && s.range in ranges ? s.range : undefined,
  }),
  component: VehicleDetail,
})

function VehicleDetail() {
  const { vehicleId } = Route.useParams()
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/vehicles/$vehicleId' })
  const can = useCan()
  const qc = useQueryClient()
  const [revealVIN, setRevealVIN] = useState(false)
  const [editing, setEditing] = useState(false)
  const [dialog, setDialog] = useState<'' | 'archive' | 'purge'>('')
  const vehicle = useQuery({
    queryKey: ['vehicle', vehicleId],
    queryFn: async () => unwrap(await api.GET('/vehicles/{vehicle}', { params: { path: { vehicle: vehicleId } } })),
  })
  const state = useQuery({
    queryKey: ['vehicle-state', vehicleId],
    queryFn: async () =>
      unwrap(await api.GET('/vehicles/{vehicle}/state', { params: { path: { vehicle: vehicleId } } })),
    enabled: can('telemetry.read'),
    refetchInterval: 60_000,
  })
  const invalidate = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: ['vehicle', vehicleId] }),
      qc.invalidateQueries({ queryKey: ['vehicles'] }),
    ])
  const archive = useMutation({
    mutationFn: async () => {
      check(await api.DELETE('/vehicles/{vehicle}', { params: { path: { vehicle: vehicleId } } }))
    },
    onSuccess: async () => {
      setDialog('')
      await invalidate()
      notifySuccess('Vehicle archived; its telemetry is kept')
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const restore = useMutation({
    mutationFn: async () =>
      unwrap(await api.POST('/vehicles/{vehicle}/restore', { params: { path: { vehicle: vehicleId } } })),
    onSuccess: async () => {
      await invalidate()
      notifySuccess('Vehicle restored')
    },
    onError: (e) => {
      notifyError(e)
    },
  })
  const [deleteVehicle, setDeleteVehicle] = useState(false)
  const purge = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.POST('/vehicles/{vehicle}/purge', {
          params: { path: { vehicle: vehicleId } },
          body: { confirm: true, delete_vehicle: deleteVehicle },
        }),
      ),
    onSuccess: async (r) => {
      setDialog('')
      notifySuccess(
        `Purged ${fmtNumber(r.observations)} observations${r.vehicle_deleted ? ' and deleted the vehicle' : ''}`,
      )
      await qc.invalidateQueries()
      if (r.vehicle_deleted) void navigate({ to: '/vehicles', search: {} })
    },
    onError: (e) => {
      notifyError(e)
    },
  })

  if (vehicle.isPending) return <LoadingBlock h={320} />
  if (vehicle.isError) return <ErrorBlock error={vehicle.error} onRetry={() => void vehicle.refetch()} />
  const v = vehicle.data
  const tab = search.tab ?? 'overview'
  const values = state.data?.values ?? []
  const fresh = values.filter((x) => !x.stale).length
  // When the car last captured data (its own timestamps) vs when WagenPulse last received a delivery for it.
  const lastReport = values
    .filter((x) => x.time_basis === 'observed')
    .reduce<string | undefined>((m, x) => (!m || x.observed_at > m ? x.observed_at : m), undefined)
  const lastReceived = values.reduce<string | undefined>(
    (m, x) => (!m || x.received_at > m ? x.received_at : m),
    undefined,
  )
  const pick = (key: string) => void navigate({ search: (s) => ({ ...s, tab: 'history', signal: key }) })

  return (
    <>
      <PageHeader
        title={
          <Group gap="sm">
            <span>{v.display_name}</span>
            {v.archived ? <StatusBadge state="archived" /> : null}
          </Group>
        }
        description={
          <Group gap={6} component="span">
            <span>
              {[brands.find((b) => b.value === v.brand)?.label, v.model, v.model_year].filter(Boolean).join(' · ')}
            </span>
            <span>·</span>
            <Text span ff="monospace" size="sm">
              {maskVIN(v.vin, revealVIN)}
            </Text>
            <Tooltip label={revealVIN ? 'Hide VIN' : 'Reveal VIN'}>
              <ActionIcon
                variant="subtle"
                size="sm"
                onClick={() => {
                  setRevealVIN((r) => !r)
                }}
                aria-label={revealVIN ? 'Hide VIN' : 'Reveal VIN'}
              >
                {revealVIN ? <IconEyeOff size={14} /> : <IconEye size={14} />}
              </ActionIcon>
            </Tooltip>
          </Group>
        }
        actions={
          can('vehicles.write') ? (
            <Group gap="xs">
              {v.archived ? (
                <Button
                  variant="default"
                  leftSection={<IconRestore size={16} />}
                  loading={restore.isPending}
                  onClick={() => {
                    restore.mutate()
                  }}
                >
                  Restore
                </Button>
              ) : (
                <Button
                  variant="default"
                  leftSection={<IconPencil size={16} />}
                  onClick={() => {
                    setEditing(true)
                  }}
                >
                  Edit
                </Button>
              )}
              <Menu position="bottom-end">
                <Menu.Target>
                  <ActionIcon variant="default" size="lg" aria-label="More vehicle actions">
                    <IconDots size={16} />
                  </ActionIcon>
                </Menu.Target>
                <Menu.Dropdown>
                  {!v.archived ? (
                    <Menu.Item
                      leftSection={<IconArchive size={16} />}
                      onClick={() => {
                        setDialog('archive')
                      }}
                    >
                      Archive
                    </Menu.Item>
                  ) : null}
                  {can('vehicles.purge') ? (
                    <Menu.Item
                      color="red"
                      leftSection={<IconTrash size={16} />}
                      onClick={() => {
                        setDialog('purge')
                      }}
                    >
                      Purge telemetry…
                    </Menu.Item>
                  ) : null}
                </Menu.Dropdown>
              </Menu>
            </Group>
          ) : null
        }
      />
      <SimpleGrid cols={{ base: 2, md: 4 }} mb="lg">
        <Kpi
          label="Car last reported"
          value={lastReport ? <Time value={lastReport} relative /> : '—'}
          hint="Capture time of the newest value"
        />
        <Kpi
          label="Last delivery"
          value={lastReceived ? <Time value={lastReceived} relative /> : '—'}
          hint="When WagenPulse received data"
        />
        <Kpi label="Signals with data" value={fmtNumber(values.length)} />
        <Kpi
          label="Up to date"
          value={`${fmtNumber(fresh)} / ${fmtNumber(values.length)}`}
          hint={state.data ? `Values newer than ${fmtDuration(state.data.stale_after_seconds)}` : undefined}
        />
      </SimpleGrid>
      <Tabs
        value={tab}
        onChange={(t) => void navigate({ search: (s) => ({ ...s, tab: (t ?? 'overview') as Search['tab'] }) })}
        keepMounted={false}
      >
        <Tabs.List mb="md">
          <Tabs.Tab value="overview">Overview</Tabs.Tab>
          <Tabs.Tab value="state">All signals</Tabs.Tab>
          <Tabs.Tab value="history">History</Tabs.Tab>
          {can('imports.read') ? <Tabs.Tab value="imports">Imports</Tabs.Tab> : null}
          {can('events.read') ? <Tabs.Tab value="events">Events</Tabs.Tab> : null}
          <Tabs.Tab value="details">Vehicle</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="overview">
          <QueryState query={state} loadingLabel="Loading the latest state…">
            {() => <VehicleOverview vehicleId={v.id} values={values} onPick={pick} />}
          </QueryState>
        </Tabs.Panel>
        <Tabs.Panel value="state">
          <Card p={0}>
            <QueryState query={state} loadingLabel="Loading the latest state…">
              {() => <StateTable values={values} onPick={pick} />}
            </QueryState>
          </Card>
        </Tabs.Panel>
        <Tabs.Panel value="history">
          <HistoryPanel
            vehicleId={v.id}
            values={values}
            signal={search.signal}
            range={search.range ?? '7d'}
            onChange={(p) => void navigate({ search: (s) => ({ ...s, ...p }) })}
          />
        </Tabs.Panel>
        <Tabs.Panel value="imports">
          <VehicleImports vehicleId={v.id} />
        </Tabs.Panel>
        <Tabs.Panel value="events">
          <VehicleEvents vehicleId={v.id} />
        </Tabs.Panel>
        <Tabs.Panel value="details">
          <Section title="Vehicle">
            <DefinitionList
              items={[
                [
                  'ID',
                  <Text key="id" ff="monospace" size="sm">
                    {v.id}
                  </Text>,
                ],
                ['Slug', v.slug],
                ['License plate', v.license_plate || '—'],
                [
                  'Tags',
                  v.tags.length
                    ? v.tags.map((t) => (
                        <Badge key={t} mr={4} variant="light">
                          {t}
                        </Badge>
                      ))
                    : '—',
                ],
                ['Stale after', v.stale_after_minutes ? `${String(v.stale_after_minutes)} min` : 'system default'],
                ['Notes', v.notes || '—'],
                ['Created', <Time key="c" value={v.created_at} />],
                ['Updated', <Time key="u" value={v.updated_at} />],
                ...(v.archived
                  ? ([['Archived', <Time key="a" value={v.archived_at} />]] as [string, ReactNode][])
                  : []),
              ]}
            />
          </Section>
        </Tabs.Panel>
      </Tabs>
      <Modal
        opened={editing}
        onClose={() => {
          setEditing(false)
        }}
        title={<Text fw={650}>Edit vehicle</Text>}
        size="lg"
      >
        <VehicleForm
          initial={v}
          submitLabel="Save changes"
          onCancel={() => {
            setEditing(false)
          }}
          onSubmit={async (body) => {
            unwrap(
              await api.PUT('/vehicles/{vehicle}', {
                params: { path: { vehicle: v.id } },
                body,
                headers: ifMatch(v.revision),
              }),
            )
            await invalidate()
            setEditing(false)
            notifySuccess('Vehicle saved')
          }}
        />
      </Modal>
      <ConfirmDialog
        opened={dialog === 'archive'}
        onClose={() => {
          setDialog('')
        }}
        onConfirm={() => {
          archive.mutate()
        }}
        loading={archive.isPending}
        title="Archive vehicle?"
        confirmLabel="Archive"
      >
        Archiving hides <b>{v.display_name}</b> from the fleet and stops collection and rule evaluation for it. Its
        telemetry and events are kept and it can be restored.
      </ConfirmDialog>
      <ConfirmDialog
        opened={dialog === 'purge'}
        onClose={() => {
          setDialog('')
        }}
        onConfirm={() => {
          purge.mutate()
        }}
        loading={purge.isPending}
        title="Purge telemetry?"
        confirmLabel="Purge permanently"
        danger
        typeToConfirm={v.slug}
      >
        <Stack gap="xs">
          <Text size="sm">
            This permanently deletes every observation, latest-state value and rollup stored for <b>{v.display_name}</b>
            . Events already published to adapters are not recalled. This cannot be undone; take a backup first if in
            doubt.
          </Text>
          <SegmentedControl
            value={deleteVehicle ? 'delete' : 'keep'}
            onChange={(x) => {
              setDeleteVehicle(x === 'delete')
            }}
            data={[
              { value: 'keep', label: 'Keep the vehicle record' },
              { value: 'delete', label: 'Also delete the vehicle' },
            ]}
          />
        </Stack>
      </ConfirmDialog>
    </>
  )
}

function StateTable({ values, onPick }: { values: StateValue[]; onPick: (key: string) => void }) {
  const [q, setQ] = useState('')
  const [cluster, setCluster] = useState<string | null>(null)
  const clusters = useMemo(() => [...new Set(values.flatMap((v) => v.signal.clusters))].sort(), [values])
  const shown = values
    .filter((v) => !cluster || v.signal.clusters.includes(cluster))
    .filter(
      (v) =>
        !q ||
        `${signalLabel(v.signal)} ${v.signal.name} ${v.signal.description ?? ''} ${v.signal.key}`
          .toLowerCase()
          .includes(q.toLowerCase()),
    )
    .sort((a, b) => signalLabel(a.signal).localeCompare(signalLabel(b.signal)))
  if (values.length === 0)
    return (
      <EmptyBlock
        title="No data yet"
        description="Import a delivery or configure a source to see this vehicle's latest values."
      />
    )
  return (
    <>
      <Group p="md" gap="sm">
        <TextInput
          leftSection={<IconSearch size={14} />}
          placeholder="Filter signals"
          value={q}
          onChange={(e) => {
            setQ(e.currentTarget.value)
          }}
          aria-label="Filter signals"
          w={260}
        />
        <Select
          placeholder="All clusters"
          data={clusters}
          value={cluster}
          onChange={setCluster}
          clearable
          aria-label="Cluster"
          w={240}
        />
        <Text size="xs" c="dimmed">
          {shown.length} of {values.length} signals
        </Text>
      </Group>
      <ScrollRegion label="Latest values">
        <Table className="wp-table" highlightOnHover data-testid="state-table" miw={720}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Signal</Table.Th>
              <Table.Th>Value</Table.Th>
              <Table.Th>Observed</Table.Th>
              <Table.Th>Freshness</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {shown.map((v) => (
              <Table.Tr
                key={v.signal.key}
                style={{ cursor: 'pointer' }}
                onClick={() => {
                  onPick(v.signal.key)
                }}
              >
                <Table.Td>
                  <Text size="sm" fw={550}>
                    {signalLabel(v.signal)}
                  </Text>
                  <Text size="xs" c="dimmed" lineClamp={1} maw={480}>
                    <Text span ff="monospace" size="xs">
                      {v.signal.name}
                    </Text>
                    {' · '}
                    {v.signal.origin === 'observed' ? 'not in the dictionary (observed key)' : v.signal.description}
                  </Text>
                </Table.Td>
                <Table.Td className="wp-num">
                  <ValueText v={v} />
                </Table.Td>
                <Table.Td>
                  <Time value={v.observed_at} relative />
                  {v.time_basis === 'delivery' ? (
                    <Tooltip label="The delivery carried no timestamp for this value; the delivery time is used.">
                      <Badge size="xs" variant="light" color="gray" ml={6}>
                        delivery time
                      </Badge>
                    </Tooltip>
                  ) : null}
                </Table.Td>
                <Table.Td>
                  <StatusBadge state={v.stale ? 'stale' : 'fresh'} />
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </ScrollRegion>
    </>
  )
}

function HistoryPanel({
  vehicleId,
  values,
  signal,
  range,
  onChange,
}: {
  vehicleId: string
  values: StateValue[]
  signal?: string
  range: string
  onChange: (p: Partial<Search>) => void
}) {
  const options = values
    .map((v) => ({ value: v.signal.key, label: signalLabel(v.signal) }))
    .sort((a, b) => a.label.localeCompare(b.label))
  const key = signal ?? values.find((v) => v.signal.value_kind === 'number')?.signal.key ?? options[0]?.value
  const selected = values.find((v) => v.signal.key === key)
  const to = useRangeEnd()
  const from = new Date(to.getTime() - (ranges[range]?.ms ?? DEFAULT_RANGE_MS))
  if (options.length === 0) return <EmptyBlock title="No signals with data" />
  return (
    <Card>
      <Group mb="md" justify="space-between" wrap="wrap">
        <Select
          searchable
          data={options}
          value={key ?? null}
          onChange={(k) => {
            if (k) onChange({ signal: k })
          }}
          label="Signal"
          w={360}
          allowDeselect={false}
        />
        <SegmentedControl
          value={range}
          onChange={(r) => {
            onChange({ range: r })
          }}
          data={Object.entries(ranges).map(([value, r]) => ({ value, label: r.label }))}
          aria-label="Time range"
        />
      </Group>
      {selected?.signal.description ? (
        <Text size="sm" c="dimmed" mb="sm">
          {selected.signal.description}
        </Text>
      ) : null}
      {key ? (
        <SeriesChart
          vehicle={vehicleId}
          signal={key}
          name={selected ? signalLabel(selected.signal) : key}
          from={from}
          to={to}
          height={320}
        />
      ) : null}
      <Group justify="flex-end" mt="sm">
        <AnchorLink to="/explorer" search={{ vehicles: [vehicleId], signals: key ? [key] : [], range }} size="sm">
          Open in the data explorer
        </AnchorLink>
      </Group>
    </Card>
  )
}

function VehicleImports({ vehicleId }: { vehicleId: string }) {
  const q = useQuery({
    queryKey: ['imports', 'vehicle', vehicleId],
    queryFn: async () => unwrap(await api.GET('/imports', { params: { query: { vehicle: vehicleId, limit: 50 } } })),
  })
  return (
    <Card p={0}>
      <QueryState query={q}>
        {() =>
          q.data?.items.length ? (
            <Table className="wp-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Import</Table.Th>
                  <Table.Th>State</Table.Th>
                  <Table.Th>Accepted</Table.Th>
                  <Table.Th>Duplicates</Table.Th>
                  <Table.Th>Coverage</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {q.data.items.map((im) => (
                  <Table.Tr key={im.id}>
                    <Table.Td>
                      <AnchorLink to="/imports/$importId" params={{ importId: im.id }} size="sm">
                        {im.kind} · <Time value={im.created_at} />
                      </AnchorLink>
                    </Table.Td>
                    <Table.Td>
                      <StatusBadge state={im.state} />
                    </Table.Td>
                    <Table.Td className="wp-num">{fmtNumber(im.observations_accepted)}</Table.Td>
                    <Table.Td className="wp-num">{fmtNumber(im.observations_duplicate)}</Table.Td>
                    <Table.Td>
                      {im.coverage_from ? (
                        <Text size="xs">
                          <Time value={im.coverage_from} /> – <Time value={im.coverage_to} />
                        </Text>
                      ) : (
                        '—'
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          ) : (
            <EmptyBlock title="No imports for this vehicle" />
          )
        }
      </QueryState>
    </Card>
  )
}

function VehicleEvents({ vehicleId }: { vehicleId: string }) {
  const q = useQuery({
    queryKey: ['events', 'vehicle', vehicleId],
    queryFn: async () => unwrap(await api.GET('/events', { params: { query: { vehicle: vehicleId, limit: 50 } } })),
  })
  return (
    <Card p={0}>
      <QueryState query={q}>
        {() =>
          q.data?.items.length ? (
            <Table className="wp-table">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Event</Table.Th>
                  <Table.Th>Observed</Table.Th>
                  <Table.Th>Deliveries</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {q.data.items.map((e) => (
                  <Table.Tr key={e.id}>
                    <Table.Td>
                      <AnchorLink to="/events/$eventId" params={{ eventId: e.id }} size="sm">
                        {e.type}
                      </AnchorLink>
                      {e.backfill ? (
                        <Badge size="xs" ml={6} variant="light" color="gray">
                          backfill
                        </Badge>
                      ) : null}
                    </Table.Td>
                    <Table.Td>
                      <Time value={e.observed_at} />
                    </Table.Td>
                    <Table.Td>
                      <DeliveryCounts d={e.deliveries} />
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          ) : (
            <EmptyBlock
              title="No events for this vehicle"
              description="Events appear when a rule matches this vehicle's observations."
            />
          )
        }
      </QueryState>
    </Card>
  )
}

function ValueText({ v }: { v: StateValue }) {
  const p = presentValue(v.signal, { ...v, unit: v.unit ?? v.signal.unit })
  const raw = v.num !== undefined && p.state ? `code ${String(v.num)}` : undefined
  return (
    <Text size="sm" c={p.muted ? 'dimmed' : p.overdue ? 'var(--wp-danger)' : undefined} title={raw}>
      {p.text}
    </Text>
  )
}
