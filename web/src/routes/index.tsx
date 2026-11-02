import { Badge, Card, Group, SimpleGrid, Stack, Table, Text, ThemeIcon } from '@mantine/core'
import { IconCar, IconCloudDownload, IconFileImport, IconPlugConnected } from '@tabler/icons-react'
import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { api, unwrap, type VehicleSummary } from '@/api/client'
import { EmptyBlock, ErrorBlock, Kpi, LoadingBlock, PageHeader, Section, StatusBadge, Time } from '@/components/ui'
import { fmtNumber, fmtRelative } from '@/lib/format'
import { useCan } from '@/lib/session'
import { AnchorLink, ButtonLink, CardLink } from '@/components/links'

export const Route = createFileRoute('/')({ component: Fleet })

function freshness(v: VehicleSummary): { state: string; label: string } {
  if (!v.freshness.last_observed_at) return { state: 'disabled', label: 'No data' }
  if (v.freshness.signals > 0 && v.freshness.stale_signals === v.freshness.signals)
    return { state: 'stale', label: 'Stale' }
  return { state: 'fresh', label: 'Fresh' }
}

function VehicleCard({ v }: { v: VehicleSummary }) {
  const f = freshness(v)
  return (
    <CardLink
      to="/vehicles/$vehicleId"
      params={{ vehicleId: v.id }}
      style={{ textDecoration: 'none', color: 'inherit' }}
      data-testid="vehicle-card"
    >
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <Group gap="sm" wrap="nowrap">
          <ThemeIcon variant="light" size={38} radius="md">
            <IconCar size={22} />
          </ThemeIcon>
          <div>
            <Text fw={650}>{v.display_name}</Text>
            <Text size="xs" c="dimmed">
              {[v.brand.replace('_', ' '), v.model, v.model_year].filter(Boolean).join(' · ')}
            </Text>
          </div>
        </Group>
        <StatusBadge state={f.state} label={f.label} />
      </Group>
      <SimpleGrid cols={3} mt="md" spacing="xs">
        <div>
          <Text size="xs" c="dimmed">
            Last data
          </Text>
          <Text size="sm" fw={600}>
            {v.freshness.last_observed_at ? fmtRelative(v.freshness.last_observed_at) : '—'}
          </Text>
        </div>
        <div>
          <Text size="xs" c="dimmed">
            Signals
          </Text>
          <Text size="sm" fw={600} className="wp-num">
            {fmtNumber(v.freshness.signals)}
          </Text>
        </div>
        <div>
          <Text size="xs" c="dimmed">
            Stale
          </Text>
          <Text
            size="sm"
            fw={600}
            className="wp-num"
            c={v.freshness.stale_signals > 0 ? 'var(--wp-warning)' : undefined}
          >
            {fmtNumber(v.freshness.stale_signals)}
          </Text>
        </div>
      </SimpleGrid>
    </CardLink>
  )
}

function Fleet() {
  const can = useCan()
  const overview = useQuery({
    queryKey: ['overview'],
    queryFn: async () => unwrap(await api.GET('/overview')),
    refetchInterval: 30_000,
  })
  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
    refetchInterval: 30_000,
  })
  const sources = useQuery({
    queryKey: ['sources'],
    queryFn: async () => unwrap(await api.GET('/sources')),
    enabled: can('sources.read'),
    refetchInterval: 30_000,
  })
  const adapters = useQuery({
    queryKey: ['adapters'],
    queryFn: async () => unwrap(await api.GET('/adapters')),
    enabled: can('adapters.read'),
    refetchInterval: 30_000,
  })
  const imports = useQuery({
    queryKey: ['imports', 'recent'],
    queryFn: async () => unwrap(await api.GET('/imports', { params: { query: { limit: 6 } } })),
    enabled: can('imports.read'),
    refetchInterval: 15_000,
  })
  if (overview.isPending || vehicles.isPending) return <LoadingBlock h={320} label="Loading the fleet…" />
  if (overview.isError) return <ErrorBlock error={overview.error} onRetry={() => void overview.refetch()} />
  if (vehicles.isError) return <ErrorBlock error={vehicles.error} onRetry={() => void vehicles.refetch()} />
  const o = overview.data
  const dead = Object.values(o.deliveries_by_adapter).reduce((n, m) => n + (m.dead ?? 0), 0)
  const retrying = Object.values(o.deliveries_by_adapter).reduce((n, m) => n + (m.retrying ?? 0), 0)
  const sourceIssues = Object.entries(o.sources_by_state)
    .filter(([s]) => !['authorized', 'configured', 'disabled'].includes(s))
    .reduce((n, [, c]) => n + c, 0)
  return (
    <>
      <PageHeader
        title="Fleet"
        description="Latest-known status, data freshness, collection and delivery health. Refreshes every 30 seconds."
        actions={
          can('imports.write') ? (
            <ButtonLink to="/imports" leftSection={<IconFileImport size={16} />} variant="default">
              Import data
            </ButtonLink>
          ) : null
        }
      />
      <SimpleGrid cols={{ base: 2, md: 4 }} mb="lg">
        <Kpi
          label="Vehicles"
          value={o.vehicles}
          hint={`${String(o.fresh_vehicles)} fresh · ${String(o.stale_vehicles)} stale · ${String(o.vehicles_without_data)} without data`}
        />
        <Kpi
          label="Observations (24 h)"
          value={fmtNumber(o.observations_24h)}
          hint="Accepted by imports in the last day"
        />
        <Kpi label="Events (24 h)" value={fmtNumber(o.events_24h)} hint="Generated by rules" />
        <Kpi
          label="Delivery issues"
          value={fmtNumber(dead + retrying)}
          color={dead > 0 ? 'var(--wp-danger)' : retrying > 0 ? 'var(--wp-warning)' : undefined}
          hint={`${String(dead)} dead · ${String(retrying)} retrying${sourceIssues ? ` · ${String(sourceIssues)} source issue(s)` : ''}`}
        />
      </SimpleGrid>
      {vehicles.data.items.length === 0 ? (
        <Card mb="lg">
          <EmptyBlock
            icon={<IconCar size={24} />}
            title="No vehicles yet"
            description="Add a vehicle with its VIN, then import deliveries downloaded from the EU Data Act portal or configure a portal source."
            action={
              can('vehicles.write') ? (
                <ButtonLink to="/vehicles" search={{ create: 1 }}>
                  Add a vehicle
                </ButtonLink>
              ) : null
            }
          />
        </Card>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} mb="lg">
          {vehicles.data.items.map((v) => (
            <VehicleCard key={v.id} v={v} />
          ))}
        </SimpleGrid>
      )}
      <SimpleGrid cols={{ base: 1, lg: 2 }}>
        {can('sources.read') ? (
          <Section
            title="Collection"
            actions={
              <AnchorLink to="/sources" size="sm">
                All sources
              </AnchorLink>
            }
          >
            {sources.isPending ? (
              <LoadingBlock h={120} />
            ) : sources.isError ? (
              <ErrorBlock error={sources.error} onRetry={() => void sources.refetch()} />
            ) : sources.data.length === 0 ? (
              <EmptyBlock
                icon={<IconCloudDownload size={24} />}
                title="No sources"
                description="Uploads work without a source. A portal source collects automatically (unofficial interface)."
              />
            ) : (
              <Table className="wp-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Source</Table.Th>
                    <Table.Th>State</Table.Th>
                    <Table.Th>Last success</Table.Th>
                    <Table.Th>Next run</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {sources.data.map((s) => (
                    <Table.Tr key={s.id}>
                      <Table.Td>
                        <AnchorLink to="/sources/$sourceId" params={{ sourceId: s.id }} size="sm">
                          {s.name}
                        </AnchorLink>
                      </Table.Td>
                      <Table.Td>
                        <StatusBadge state={s.status?.state ?? 'configured'} />
                      </Table.Td>
                      <Table.Td>
                        <Time value={s.status?.last_success_at} relative />
                      </Table.Td>
                      <Table.Td>
                        <Time value={s.status?.next_run_at} relative />
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Section>
        ) : null}
        {can('adapters.read') ? (
          <Section
            title="Delivery"
            actions={
              <AnchorLink to="/adapters" size="sm">
                All adapters
              </AnchorLink>
            }
          >
            {adapters.isPending ? (
              <LoadingBlock h={120} />
            ) : adapters.isError ? (
              <ErrorBlock error={adapters.error} onRetry={() => void adapters.refetch()} />
            ) : adapters.data.length === 0 ? (
              <EmptyBlock
                icon={<IconPlugConnected size={24} />}
                title="No adapters"
                description="Add a Pulse/NATS adapter to publish events."
              />
            ) : (
              <Table className="wp-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Adapter</Table.Th>
                    <Table.Th>Connection</Table.Th>
                    <Table.Th>Pending</Table.Th>
                    <Table.Th>Retrying</Table.Th>
                    <Table.Th>Dead</Table.Th>
                    <Table.Th>Delivered 24 h</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {adapters.data.map((a) => (
                    <Table.Tr key={a.id}>
                      <Table.Td>
                        <AnchorLink to="/adapters/$adapterId" params={{ adapterId: a.id }} size="sm">
                          {a.name}
                        </AnchorLink>
                      </Table.Td>
                      <Table.Td>
                        <StatusBadge state={a.status?.state ?? 'connecting'} />
                      </Table.Td>
                      <Table.Td className="wp-num">{a.queue?.pending ?? 0}</Table.Td>
                      <Table.Td className="wp-num">{a.queue?.retrying ?? 0}</Table.Td>
                      <Table.Td className="wp-num">
                        {(a.queue?.dead ?? 0) > 0 ? <Badge color="red">{a.queue?.dead}</Badge> : 0}
                      </Table.Td>
                      <Table.Td className="wp-num">{a.queue?.succeeded_24h ?? 0}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Section>
        ) : null}
        {can('imports.read') ? (
          <Section
            title="Recent imports"
            actions={
              <AnchorLink to="/imports" size="sm">
                All imports
              </AnchorLink>
            }
          >
            {imports.isPending ? (
              <LoadingBlock h={120} />
            ) : imports.isError ? (
              <ErrorBlock error={imports.error} onRetry={() => void imports.refetch()} />
            ) : imports.data.items.length === 0 ? (
              <EmptyBlock icon={<IconFileImport size={24} />} title="No imports yet" />
            ) : (
              <Stack gap={0}>
                {imports.data.items.map((im) => (
                  <Group
                    key={im.id}
                    justify="space-between"
                    px="lg"
                    py={8}
                    style={{ borderTop: '1px solid var(--wp-border)' }}
                    wrap="nowrap"
                  >
                    <Group gap="sm" wrap="nowrap">
                      <StatusBadge state={im.state} />
                      <AnchorLink to="/imports/$importId" params={{ importId: im.id }} size="sm">
                        {im.kind} · {fmtNumber(im.observations_accepted)} observations
                      </AnchorLink>
                    </Group>
                    <Text size="xs" c="dimmed">
                      <Time value={im.created_at} relative />
                    </Text>
                  </Group>
                ))}
              </Stack>
            )}
          </Section>
        ) : null}
      </SimpleGrid>
    </>
  )
}
