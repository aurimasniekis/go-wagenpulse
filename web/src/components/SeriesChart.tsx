import { Badge, Group, SegmentedControl, Table, Text, Tooltip } from '@mantine/core'
import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { api, unwrap, type Series } from '@/api/client'
import { fmtDateTime, fmtNumber, fmtValue } from '@/lib/format'
import { displayNumber, displayUnit } from '@/lib/signals'
import { chartBase, EChart, usePalette, type EChartsOption } from './EChart'
import { EmptyBlock, ErrorBlock, LoadingBlock, ScrollRegion } from './ui'

const roundedNow = () => new Date(Math.ceil(Date.now() / 60_000) * 60_000)

/**
 * End of a "last N" chart range: now rounded up to the minute (stable query keys), advanced every five minutes so
 * long-open pages keep showing recent data.
 */
export function useRangeEnd(): Date {
  const [end, setEnd] = useState(roundedNow)
  useEffect(() => {
    const t = setInterval(() => {
      setEnd(roundedNow())
    }, 300_000)
    return () => {
      clearInterval(t)
    }
  }, [])
  return end
}

/** Default chart range: 7 days. */
export const DEFAULT_RANGE_MS = 7 * 86400_000

export const ranges: Record<string, { label: string; ms: number }> = {
  '6h': { label: '6 h', ms: 6 * 3600_000 },
  '24h': { label: '24 h', ms: 24 * 3600_000 },
  '7d': { label: '7 d', ms: 7 * 86400_000 },
  '30d': { label: '30 d', ms: 30 * 86400_000 },
  '90d': { label: '90 d', ms: 90 * 86400_000 },
  '365d': { label: '1 y', ms: 365 * 86400_000 },
}

export function useSeries(vehicle: string, signal: string, from: Date, to: Date, maxPoints = 1500) {
  return useQuery({
    queryKey: ['series', vehicle, signal, from.toISOString(), to.toISOString(), maxPoints],
    queryFn: async () =>
      toDisplaySeries(
        unwrap(
          await api.GET('/vehicles/{vehicle}/series', {
            params: {
              path: { vehicle },
              query: { signal, from: from.toISOString(), to: to.toISOString(), max_points: maxPoints },
            },
          }),
        ),
      ),
    enabled: Boolean(vehicle && signal),
    placeholderData: (prev) => prev,
  })
}

/** Converts a series' values into the display unit (e.g. deci-Kelvin → °C); stored data is unchanged. */
export function toDisplaySeries(s: Series): Series {
  if (displayUnit(s.unit) === (s.unit ?? '')) return s
  const conv = (n: number | null | undefined) => (n === null || n === undefined ? n : displayNumber(s.unit, n))
  return {
    ...s,
    unit: displayUnit(s.unit),
    points: s.points.map((p) => ({
      ...p,
      value: conv(p.value) ?? null,
      min: conv(p.min) ?? undefined,
      max: conv(p.max) ?? undefined,
    })),
  }
}

/** Converts a series to chart options. Gaps stay gaps: empty buckets are null, never interpolated. */
export function seriesOption(s: Series, base: EChartsOption, name: string, color?: string): EChartsOption {
  const numeric = s.aggregation !== 'changes'
  const data = s.points.map((p) => [p.t, numeric ? (p.value ?? null) : (p.value ?? null)])
  const out: EChartsOption = {
    ...base,
    tooltip: {
      ...(base.tooltip as object),
      valueFormatter: (v: unknown) =>
        typeof v === 'number' ? `${fmtNumber(v)}${s.unit ? ` ${s.unit}` : ''}` : 'no data',
    },
    yAxis: { ...(base.yAxis as object), name: s.unit || undefined, nameTextStyle: { align: 'right' } },
    series: [
      {
        name,
        type: 'line',
        step: s.aggregation === 'changes' ? 'end' : undefined,
        showSymbol: s.points.length < 60,
        symbolSize: 5,
        connectNulls: false,
        sampling: undefined,
        data,
        lineStyle: { width: 1.6, color },
        itemStyle: color ? { color } : undefined,
      },
    ],
  }
  if (s.aggregation === 'bucket') {
    const lower = s.points.map((p) => [p.t, p.min ?? null])
    const band = s.points.map((p) => [p.t, p.max !== undefined && p.min !== undefined ? p.max - p.min : null])
    ;(out.series as object[]).push(
      {
        name: 'min',
        type: 'line',
        data: lower,
        lineStyle: { opacity: 0 },
        stack: 'band',
        symbol: 'none',
        connectNulls: false,
        tooltip: { show: false },
      },
      {
        name: 'range',
        type: 'line',
        data: band,
        lineStyle: { opacity: 0 },
        areaStyle: { opacity: 0.18, color },
        stack: 'band',
        symbol: 'none',
        connectNulls: false,
        tooltip: { show: false },
      },
    )
  }
  return out
}

/** One signal's history for a vehicle with chart/table views. */
export function SeriesChart({
  vehicle,
  signal,
  name,
  from,
  to,
  height = 300,
  compact = false,
}: {
  vehicle: string
  signal: string
  name: string
  from: Date
  to: Date
  height?: number
  /** Chart only: no aggregation badges and no chart/table switch (dashboard tiles). */
  compact?: boolean
}) {
  const q = useSeries(vehicle, signal, from, to)
  const palette = usePalette()
  const [view, setView] = useState<'chart' | 'table'>('chart')
  const option = useMemo(
    () => (q.data ? seriesOption(q.data, chartBase(palette), name, palette.series[0]) : null),
    [q.data, palette, name],
  )
  if (q.isPending) return <LoadingBlock h={height} label="Loading history…" />
  if (q.isError) return <ErrorBlock error={q.error} onRetry={() => void q.refetch()} />
  const s = q.data
  const withValues = s.points.filter((p) => p.count > 0).length
  if (compact) {
    return withValues === 0 ? (
      <Text size="sm" c="dimmed">
        No data in the last 30 days.
      </Text>
    ) : option ? (
      <EChart option={option} height={height} label={`${name} history chart`} />
    ) : null
  }
  return (
    <div>
      <Group justify="space-between" mb="xs" wrap="wrap" gap="xs">
        <Group gap={6}>
          <Badge variant="light" color="gray">
            {s.aggregation === 'raw'
              ? 'raw values'
              : s.aggregation === 'bucket'
                ? `${fmtNumber(s.bucket_seconds ?? 0)} s buckets (avg, min–max)`
                : 'changes'}
          </Badge>
          {s.gaps > 0 ? (
            <Tooltip label="Buckets without observations are shown as gaps, never interpolated.">
              <Badge variant="light" color="yellow">
                {s.gaps} gap{s.gaps === 1 ? '' : 's'}
              </Badge>
            </Tooltip>
          ) : null}
          {s.truncated ? (
            <Badge variant="light" color="orange">
              truncated
            </Badge>
          ) : null}
          {s.source === 'rollups' ? (
            <Badge variant="light" color="gray">
              hourly rollups
            </Badge>
          ) : null}
        </Group>
        <SegmentedControl
          size="xs"
          value={view}
          onChange={(v) => {
            setView(v as 'chart' | 'table')
          }}
          data={[
            { value: 'chart', label: 'Chart' },
            { value: 'table', label: 'Table' },
          ]}
          aria-label="View"
        />
      </Group>
      {withValues === 0 ? (
        <EmptyBlock
          title="No data in this range"
          description="Pick a longer range or check the vehicle's imports and sources."
        />
      ) : view === 'chart' && option ? (
        <EChart option={option} height={height} label={`${name} history chart`} />
      ) : (
        <ScrollRegion label="History values" maxHeight={height + 60}>
          <Table className="wp-table" stickyHeader>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Time</Table.Th>
                <Table.Th>{s.aggregation === 'bucket' ? 'Average' : 'Value'}</Table.Th>
                {s.aggregation === 'bucket' ? (
                  <>
                    <Table.Th>Min</Table.Th>
                    <Table.Th>Max</Table.Th>
                    <Table.Th>Samples</Table.Th>
                  </>
                ) : (
                  <Table.Th>Quality</Table.Th>
                )}
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {s.points
                .slice()
                .reverse()
                .map((p) => (
                  <Table.Tr key={p.t}>
                    <Table.Td className="wp-num">{fmtDateTime(p.t)}</Table.Td>
                    <Table.Td className="wp-num">
                      {p.count === 0 ? (
                        <Text c="dimmed" size="sm">
                          gap
                        </Text>
                      ) : (
                        fmtValue({ num: p.value, text: p.text, quality: p.quality ?? 'ok', unit: s.unit })
                      )}
                    </Table.Td>
                    {s.aggregation === 'bucket' ? (
                      <>
                        <Table.Td className="wp-num">{fmtNumber(p.min)}</Table.Td>
                        <Table.Td className="wp-num">{fmtNumber(p.max)}</Table.Td>
                        <Table.Td className="wp-num">{p.count}</Table.Td>
                      </>
                    ) : (
                      <Table.Td>{p.quality ?? 'ok'}</Table.Td>
                    )}
                  </Table.Tr>
                ))}
            </Table.Tbody>
          </Table>
        </ScrollRegion>
      )}
    </div>
  )
}
