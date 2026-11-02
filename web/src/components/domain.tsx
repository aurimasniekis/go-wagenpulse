import { Badge, Group, Text } from '@mantine/core'
import type { Definition } from '@/api/client'

/** Per-state delivery counts for an event. */
export function DeliveryCounts({
  d,
}: {
  d: { total: number; succeeded: number; pending: number; retrying: number; dead: number }
}) {
  if (d.total === 0)
    return (
      <Text size="xs" c="dimmed">
        no routes matched
      </Text>
    )
  return (
    <Group gap={4}>
      {d.succeeded ? (
        <Badge color="green" variant="light">
          {d.succeeded} delivered
        </Badge>
      ) : null}
      {d.pending ? (
        <Badge color="blue" variant="light">
          {d.pending} pending
        </Badge>
      ) : null}
      {d.retrying ? (
        <Badge color="yellow" variant="light">
          {d.retrying} retrying
        </Badge>
      ) : null}
      {d.dead ? (
        <Badge color="red" variant="light">
          {d.dead} dead
        </Badge>
      ) : null}
    </Group>
  )
}

/** Event policies for imports and collection (ADR: uploads and history default to store-only). */
export const eventPolicies = [
  { value: 'none', label: 'Store only (no events)' },
  { value: 'preview', label: 'Preview events (not delivered)' },
  { value: 'publish', label: 'Publish events (marked backfill)' },
]

/** One-line, human-readable summary of a rule definition. */
export function describeRule(d: Definition): string {
  const signals =
    d.signal_keys.length === 0
      ? 'any signal'
      : d.signal_keys.length === 1
        ? '1 signal'
        : `${String(d.signal_keys.length)} signals`
  const vehicles =
    d.vehicle_ids.length === 0
      ? 'all vehicles'
      : d.vehicle_ids.length === 1
        ? '1 vehicle'
        : `${String(d.vehicle_ids.length)} vehicles`
  let what = ''
  switch (d.kind) {
    case 'threshold':
      what = `${d.direction === 'below' ? 'below' : 'above'} ${String(d.threshold ?? 0)}${d.hysteresis ? ` (±${String(d.hysteresis)})` : ''}`
      break
    case 'signal_change':
      what = d.deadband ? `changes by ≥ ${String(d.deadband)}` : 'any change'
      break
    case 'transition':
      what = `${d.from?.length ? d.from.join('/') : 'any'} → ${d.to?.length ? d.to.join('/') : 'any'}`
      break
    case 'stale':
      what = `no data for ${String(Math.round((d.stale_after_seconds ?? 0) / 60))} min`
      break
  }
  return `${what} · ${signals} · ${vehicles}`
}

/** Event types produced by rules (envelope v1). */
export const eventTypes = [
  'signal.changed',
  'threshold.crossed',
  'threshold.cleared',
  'state.transition',
  'data.stale',
  'data.resumed',
]
