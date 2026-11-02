import { Alert, Button, Card, Group, MultiSelect, SegmentedControl, SimpleGrid, Stack, Text } from '@mantine/core'
import { IconDownload, IconInfoCircle } from '@tabler/icons-react'
import { useQueries, useQuery } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { api, exportUrl, unwrap, type Series } from '@/api/client'
import { chartBase, EChart, usePalette, type EChartsOption } from '@/components/EChart'
import { DEFAULT_RANGE_MS, ranges, useRangeEnd } from '@/components/SeriesChart'
import { EmptyBlock, ErrorBlock, LoadingBlock, PageHeader, Section } from '@/components/ui'
import { fmtNumber } from '@/lib/format'
import { useCan } from '@/lib/session'

interface Search {
  vehicles?: string[]
  signals?: string[]
  range?: string
}

const strList = (v: unknown): string[] | undefined =>
  Array.isArray(v)
    ? v.filter((x): x is string => typeof x === 'string').slice(0, 8)
    : typeof v === 'string'
      ? [v]
      : undefined

export const Route = createFileRoute('/explorer')({
  validateSearch: (s: Record<string, unknown>): Search => ({
    vehicles: strList(s.vehicles),
    signals: strList(s.signals),
    range: typeof s.range === 'string' && s.range in ranges ? s.range : undefined,
  }),
  component: Explorer,
})

const MAX_SIGNALS = 6

function Explorer() {
  const search = Route.useSearch()
  const navigate = useNavigate({ from: '/explorer' })
  const can = useCan()
  const palette = usePalette()
  const range = search.range ?? '7d'
  const vehicleIds = search.vehicles ?? []
  const signalKeys = search.signals ?? []
  const to = useRangeEnd()
  const from = new Date(to.getTime() - (ranges[range]?.ms ?? DEFAULT_RANGE_MS))

  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
  })
  const signals = useQuery({
    queryKey: ['signals', 'seen'],
    queryFn: async () => unwrap(await api.GET('/signals', { params: { query: { only_seen: true, limit: 200 } } })),
  })
  const pairs = vehicleIds.flatMap((v) => signalKeys.map((s) => ({ v, s })))
  const series = useQueries({
    queries: pairs.map(({ v, s }) => ({
      queryKey: ['series', v, s, from.toISOString(), to.toISOString(), 1000],
      queryFn: async () =>
        unwrap(
          await api.GET('/vehicles/{vehicle}/series', {
            params: {
              path: { vehicle: v },
              query: { signal: s, from: from.toISOString(), to: to.toISOString(), max_points: 1000 },
            },
          }),
        ),
    })),
  })

  const vName = (id: string) => vehicles.data?.items.find((x) => x.id === id)?.display_name ?? id
  const sInfo = (key: string) => signals.data?.items.find((x) => x.key === key)

  if (vehicles.isPending || signals.isPending) return <LoadingBlock h={320} />
  if (vehicles.isError) return <ErrorBlock error={vehicles.error} />
  if (signals.isError) return <ErrorBlock error={signals.error} />

  const set = (p: Partial<Search>) => void navigate({ search: (s) => ({ ...s, ...p }), replace: true })
  const loading = series.some((q) => q.isPending)
  const failed = series.find((q) => q.isError)

  return (
    <>
      <PageHeader
        title="Data explorer"
        description="Compare signals across vehicles. Long ranges are bucketed server-side (average with min–max band); empty buckets stay gaps."
        actions={
          can('telemetry.export') && pairs.length > 0 ? (
            <Button
              component="a"
              href={exportUrl(vehicleIds, signalKeys, from, to)}
              variant="default"
              leftSection={<IconDownload size={16} />}
              download
            >
              Export CSV
            </Button>
          ) : null
        }
      />
      <Card mb="lg">
        <SimpleGrid cols={{ base: 1, md: 3 }}>
          <MultiSelect
            label="Vehicles"
            data={vehicles.data.items.map((v) => ({ value: v.id, label: v.display_name }))}
            value={vehicleIds}
            onChange={(v) => {
              set({ vehicles: v })
            }}
            searchable
            maxValues={4}
            placeholder={vehicleIds.length ? undefined : 'Pick vehicles'}
          />
          <MultiSelect
            label={`Signals (up to ${String(MAX_SIGNALS)})`}
            data={signals.data.items.map((s) => ({ value: s.key, label: s.name }))}
            value={signalKeys}
            onChange={(v) => {
              set({ signals: v })
            }}
            searchable
            maxValues={MAX_SIGNALS}
            placeholder={signalKeys.length ? undefined : 'Pick signals'}
            nothingFoundMessage="No signal with data matches"
          />
          <Stack gap={4}>
            <Text size="sm" fw={500}>
              Range
            </Text>
            <SegmentedControl
              value={range}
              onChange={(r) => {
                set({ range: r })
              }}
              data={Object.entries(ranges).map(([value, r]) => ({ value, label: r.label }))}
              aria-label="Time range"
            />
          </Stack>
        </SimpleGrid>
      </Card>
      {signals.data.items.length === 0 ? (
        <Alert icon={<IconInfoCircle size={18} />} variant="light">
          No signal has data yet. Import a delivery first.
        </Alert>
      ) : pairs.length === 0 ? (
        <Card>
          <EmptyBlock
            title="Pick at least one vehicle and one signal"
            description="The selection is kept in the URL so you can bookmark or share the view."
          />
        </Card>
      ) : loading ? (
        <LoadingBlock h={320} label="Loading series…" />
      ) : failed ? (
        <ErrorBlock error={failed.error} />
      ) : (
        <Stack>
          {signalKeys.map((key) => {
            const info = sInfo(key)
            const rows = vehicleIds
              .map((v) => ({ v, s: series[pairs.findIndex((p) => p.v === v && p.s === key)]?.data }))
              .filter((x): x is { v: string; s: Series } => Boolean(x.s))
            const option: EChartsOption = {
              ...chartBase(palette),
              legend: { show: vehicleIds.length > 1, top: 0, textStyle: { color: palette.text } },
              yAxis: { ...(chartBase(palette).yAxis as object), name: rows[0]?.s.unit || undefined },
              series: rows.map(({ v, s }, i) => ({
                name: vName(v),
                type: 'line',
                step: s.aggregation === 'changes' ? 'end' : undefined,
                connectNulls: false,
                showSymbol: s.points.length < 60,
                symbolSize: 5,
                data: s.points.map((p) => [p.t, p.value ?? null]),
                lineStyle: { width: 1.6 },
                color: palette.series[i % palette.series.length],
              })),
            }
            const gaps = rows.reduce((n, r) => n + r.s.gaps, 0)
            const points = rows.reduce((n, r) => n + r.s.points.filter((p) => p.count > 0).length, 0)
            return (
              <Section
                key={key}
                padded
                title={info?.name ?? key}
                description={`${info?.description ?? ''}${gaps ? ` · ${fmtNumber(gaps)} empty bucket(s) shown as gaps` : ''}`}
              >
                <div>
                  {points === 0 ? (
                    <EmptyBlock title="No data in this range" />
                  ) : (
                    <EChart option={option} height={280} label={`${info?.name ?? key} across vehicles`} />
                  )}
                </div>
              </Section>
            )
          })}
        </Stack>
      )}
      <Group mt="md">
        <Text size="xs" c="dimmed">
          CSV exports contain raw observations (not buckets), at most 100,000 rows per export.
        </Text>
      </Group>
    </>
  )
}
